import { app, BrowserWindow, ipcMain, session } from 'electron';
import { spawn, ChildProcess } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { dirname, join } from 'node:path';
import { IPC, JoinerSettings, EgressDescriptor, EgressProbeResult } from '../constants';

// Single global joiner process. We never run two tunnels at once: the
// wintun adapter and the route table are exclusive resources.
let joinerProcess: ChildProcess | null = null;
let mainWindow: BrowserWindow | null = null;
let captchaWindow: BrowserWindow | null = null;
let yandexLoginWindow: BrowserWindow | null = null;
let userRequestedStop = false;
let switchingToWorkSession = false;
let reconnectTimer: NodeJS.Timeout | null = null;
let retryCount = 0;
let lastSettings: JoinerSettings | null = null;
const MAX_RETRIES = 8;
const YANDEX_LOGIN_URL = 'https://passport.yandex.ru/auth?retpath=https%3A%2F%2Ftelemost.yandex.ru%2F';
const YANDEX_COOKIE_DOMAINS = ['yandex.ru', 'yandex.net', 'ya.ru'];

function serviceUserId(): string {
  const dir = join(app.getPath('userData'), 'service');
  const file = join(dir, 'identity');
  try {
    const value = readFileSync(file, 'utf8').trim();
    if (/^[0-9a-f]{8}-[0-9a-f-]{27}$/i.test(value)) return value;
  } catch {}
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const value = randomUUID();
  const temporary = `${file}.tmp`;
  writeFileSync(temporary, value, { encoding: 'utf8', mode: 0o600 });
  renameSync(temporary, file);
  chmodSync(file, 0o600);
  return value;
}

function serviceCookieFile(): string {
  return join(app.getPath('userData'), 'service', serviceUserId(), 'cookies-telemost.json');
}

function hasServiceCookies(): boolean {
  try {
    const cookies = JSON.parse(readFileSync(serviceCookieFile(), 'utf8')) as Array<{ name?: string; value?: string }>;
    return Array.isArray(cookies) && cookies.some((cookie) => cookie.name === 'Session_id' && Boolean(cookie.value));
  } catch {
    return false;
  }
}

function yandexSession() {
  return session.fromPartition(`persist:joiner-yandex-${serviceUserId()}`);
}

async function exportYandexCookies(): Promise<boolean> {
  const cookies = (await yandexSession().cookies.get({})).filter((cookie) =>
    Boolean(cookie.domain && YANDEX_COOKIE_DOMAINS.some((domain) => cookie.domain!.includes(domain))),
  );
  if (!cookies.some((cookie) => cookie.name === 'Session_id' && cookie.value)) return false;
  const target = serviceCookieFile();
  mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
  const temporary = `${target}.tmp`;
  try {
    writeFileSync(temporary, JSON.stringify(cookies), { encoding: 'utf8', mode: 0o600 });
    renameSync(temporary, target);
    chmodSync(target, 0o600);
  } finally {
    rmSync(temporary, { force: true });
  }
  return true;
}

async function openYandexLogin(): Promise<{ ok: boolean; error?: string }> {
  if (await exportYandexCookies()) return { ok: true };
  if (yandexLoginWindow && !yandexLoginWindow.isDestroyed()) {
    yandexLoginWindow.focus();
    return { ok: false, error: 'Yandex login is already open' };
  }
  return new Promise((resolve) => {
    const ses = yandexSession();
    let settled = false;
    const finish = (result: { ok: boolean; error?: string }) => {
      if (settled) return;
      settled = true;
      ses.cookies.removeListener('changed', onCookieChanged);
      resolve(result);
    };
    const onCookieChanged = async (_event: Electron.Event, cookie: Electron.Cookie, cause: string, removed: boolean) => {
      if (removed || cause === 'expired-overwrite' || cookie.name !== 'Session_id') return;
      if (!cookie.domain || !YANDEX_COOKIE_DOMAINS.some((domain) => cookie.domain!.includes(domain))) return;
      try {
        if (!(await exportYandexCookies())) return;
        finish({ ok: true });
        yandexLoginWindow?.close();
      } catch (error) {
        finish({ ok: false, error: (error as Error).message });
      }
    };
    ses.cookies.on('changed', onCookieChanged);
    yandexLoginWindow = new BrowserWindow({
      width: 520,
      height: 700,
      title: 'Yandex sign in',
      parent: mainWindow ?? undefined,
      autoHideMenuBar: true,
      webPreferences: {
        partition: `persist:joiner-yandex-${serviceUserId()}`,
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
      },
    });
    yandexLoginWindow.webContents.on('did-fail-load', (_event, code, description, _url, isMainFrame) => {
      if (!isMainFrame || code === -3) return;
      finish({ ok: false, error: `Yandex sign in failed: ${description}` });
      yandexLoginWindow?.close();
    });
    void yandexLoginWindow.loadURL(YANDEX_LOGIN_URL).catch((error) => {
      finish({ ok: false, error: `Yandex sign in failed: ${(error as Error).message}` });
      yandexLoginWindow?.close();
    });
    yandexLoginWindow.on('closed', () => {
      yandexLoginWindow = null;
      finish({ ok: false, error: 'Yandex sign in was cancelled' });
    });
  });
}

interface ServiceSessionReady {
  requestId: string;
  sessionId: string;
  joinLink: string;
  egressId: string;
  ttlSeconds: number;
}

function openCaptchaWindow(url: string) {
  if (captchaWindow && !captchaWindow.isDestroyed()) {
    captchaWindow.loadURL(url);
    captchaWindow.focus();
    return;
  }
  captchaWindow = new BrowserWindow({
    width: 520,
    height: 640,
    title: 'Solve the captcha',
    parent: mainWindow ?? undefined,
    autoHideMenuBar: true,
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
  });
  captchaWindow.loadURL(url);
  captchaWindow.on('closed', () => { captchaWindow = null; });
}

function closeCaptchaWindow() {
  if (captchaWindow && !captchaWindow.isDestroyed()) {
    captchaWindow.close();
  }
  captchaWindow = null;
}

function resolveJoinerExe(): string {
  // When packaged, electron-builder copies the backend binary into
  // resources/ under the OS-appropriate name. In dev, fall back to
  // the per-arch artifact next to the Go source.
  const exeName = process.platform === 'win32' ? 'desktop-joiner.exe' : 'desktop-joiner';
  const packaged = join(process.resourcesPath || '', exeName);
  if (existsSync(packaged)) return packaged;

  const baseDir = join(__dirname, '..', '..', 'desktop-joiner');
  if (process.platform === 'darwin') {
    return join(baseDir, 'desktop-joiner-darwin');
  }
  const archMap: Record<string, string> = { x64: 'x64', arm64: 'arm64', ia32: 'ia32' };
  const archTag = archMap[process.arch] ?? 'x64';
  const suffix = process.platform === 'win32' ? '.exe' : '';
  const platTag = process.platform === 'win32' ? 'windows' : 'linux';
  return join(baseDir, `desktop-joiner-${platTag}-${archTag}${suffix}`);
}

function send(channel: string, payload: unknown) {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.webContents.send(channel, payload);
  }
}

function detectPlatformFromLink(link: string): JoinerSettings['platform'] {
  const lower = link.toLowerCase();
  if (lower.startsWith('wbstream://') || lower.includes('stream.wb.ru')) return 'wbstream';
  if (lower.includes('telemost.yandex')) return 'telemost';
  if (lower.startsWith('dion://') || lower.includes('dion.vc')) return 'dion';
  return 'vk';
}

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 900,
    height: 600,
    title: 'VConnect Joiner',
    icon: join(__dirname, '..', '..', 'resources', 'icon.png'),
    webPreferences: {
      preload: join(__dirname, '..', 'preload', 'index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
    },
  });
  mainWindow.setMenuBarVisibility(false);
  mainWindow.loadFile(join(__dirname, '..', '..', 'index.html'));
}

app.whenReady().then(() => {
  createWindow();
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  stopJoiner();
  if (process.platform !== 'darwin') app.quit();
});

function spawnJoiner(settings: JoinerSettings): { ok: boolean; error?: string } {
  const exe = resolveJoinerExe();
  if (!existsSync(exe)) {
    return { ok: false, error: `desktop-joiner binary not found at ${exe}` };
  }
  const tunSupported =
    process.platform === 'win32' || process.platform === 'linux' || process.platform === 'darwin';
  const noTun = tunSupported ? settings.noTun : true;
  if (process.platform !== 'win32' && !noTun && process.getuid && process.getuid() !== 0) {
    send(IPC.LOG, `[main] WARNING: ${process.platform} TUN routing needs root; relaunch with sudo or untick the TUN option\n`);
  }
  const args = [
    '--platform', settings.platform,
    '--link', settings.link,
    '--name', settings.displayName,
    '--socks-port', String(settings.socksPort),
    '--tunnel-mode', settings.tunnelMode,
    '--vp8-fps', String(settings.vp8Fps),
    '--vp8-batch', String(settings.vp8Batch),
    '--resources', settings.resources,
    '--dns', settings.dns,
  ];
  if (settings.socksUser) args.push('--socks-user', settings.socksUser);
  if (settings.socksPass) args.push('--socks-pass', settings.socksPass);
  if (settings.egressId) args.push('--egress-id', settings.egressId);
  if (settings.serviceControl) {
    args.push('--service-control');
    args.push('--service-user-id', serviceUserId());
    if (settings.serviceWorkPlatform === 'telemost' && hasServiceCookies()) {
      args.push('--service-cookie-file', serviceCookieFile());
      args.push('--service-cookie-platform', 'telemost');
    }
    if (settings.serviceWorkPlatform) args.push('--service-work-platform', settings.serviceWorkPlatform);
  }
  if (noTun) args.push('--no-tun');
  if (settings.dualTrack && (settings.platform === 'vk' || settings.platform === 'wbstream')) {
    args.push('--dual-track');
  }

  const elevateOnLinux =
    process.platform === 'linux' && !noTun &&
    process.getuid && process.getuid() !== 0;
  const spawnCmd = elevateOnLinux ? 'pkexec' : exe;
  const spawnArgs = elevateOnLinux ? [exe, ...args] : args;
  const loggedArgs = [...spawnArgs];
  const cookiePathAt = loggedArgs.indexOf('--service-cookie-file');
  if (cookiePathAt >= 0 && loggedArgs[cookiePathAt + 1]) loggedArgs[cookiePathAt + 1] = '<private-cookie-file>';
  const commandLine = [spawnCmd, ...loggedArgs].map((s) => (/\s/.test(s) ? `"${s}"` : s)).join(' ');
  send(IPC.LOG, `[main] spawning: ${commandLine}\n`);
  try {
    joinerProcess = spawn(spawnCmd, spawnArgs, { windowsHide: true });
  } catch (err) {
    return { ok: false, error: `spawn failed: ${(err as Error).message}` };
  }
  send(IPC.RUNNING, true);
  send(IPC.STATUS, 'starting');

  joinerProcess.on('error', (err) => {
    send(IPC.LOG, `[main] spawn error: ${err.message}\n`);
    send(IPC.STATUS, 'stopped');
    send(IPC.RUNNING, false);
    joinerProcess = null;
  });
  const parseDiscoveryLine = (line: string) => {
    const listMarker = 'EGRESS_LIST:';
    const probeMarker = 'EGRESS_PROBE:';
    const serviceMarker = 'SERVICE_SESSION_READY:';
    try {
      const serviceAt = line.indexOf(serviceMarker);
      if (serviceAt >= 0) {
        const session = JSON.parse(line.slice(serviceAt + serviceMarker.length)) as ServiceSessionReady;
        send(IPC.LOG, `[main] service returned work call ${session.sessionId} via ${session.egressId}\n`);
        switchToWorkSession(session);
        return;
      }
      const listAt = line.indexOf(listMarker);
      if (listAt >= 0) {
        const payload = JSON.parse(line.slice(listAt + listMarker.length)) as { egresses: EgressDescriptor[] };
        send(IPC.EGRESS_LIST, payload.egresses);
      }
      const probeAt = line.indexOf(probeMarker);
      if (probeAt >= 0) {
        send(IPC.EGRESS_PROBE, JSON.parse(line.slice(probeAt + probeMarker.length)) as EgressProbeResult);
      }
    } catch (err) {
      send(IPC.LOG, `[main] invalid egress discovery payload: ${(err as Error).message}\n`);
    }
  };
  const makeOutputHandler = () => {
    let pending = '';
    return (text: string) => {
      send(IPC.LOG, text);
      pending += text;
      const lines = pending.split(/\r?\n/);
      pending = lines.pop() ?? '';
      for (const line of lines) {
        parseDiscoveryLine(line);
      }
      if (text.includes('TUNNEL ACTIVE')) send(IPC.STATUS, 'active');
      if (text.includes('TUNNEL CONNECTED')) {
        send(IPC.STATUS, 'connected');
        retryCount = 0;
      }
      const captchaMatch = text.match(/STATUS:CAPTCHA:(\S+)/);
      if (captchaMatch) {
        openCaptchaWindow(captchaMatch[1]);
      } else if (captchaWindow && /captcha solved|Auth complete|TUNNEL/i.test(text)) {
        closeCaptchaWindow();
      }
    };
  };
  const handleStdout = makeOutputHandler();
  const handleStderr = makeOutputHandler();
  joinerProcess.stdout?.on('data', (b: Buffer) => handleStdout(b.toString()));
  joinerProcess.stderr?.on('data', (b: Buffer) => handleStderr(b.toString()));
  joinerProcess.on('exit', (code, signal) => {
    closeCaptchaWindow();
    send(IPC.LOG, `\n[main] joiner exited code=${code} signal=${signal}\n`);
    send(IPC.STATUS, 'stopped');
    send(IPC.RUNNING, false);
    joinerProcess = null;

    if (userRequestedStop || switchingToWorkSession || !lastSettings) return;
    if (retryCount >= MAX_RETRIES) {
      send(IPC.LOG, `[main] auto-reconnect: giving up after ${MAX_RETRIES} attempts\n`);
      return;
    }
    retryCount++;
    const delayMs = Math.min(30_000, 2_000 * 2 ** (retryCount - 1));
    send(IPC.LOG, `[main] auto-reconnect attempt ${retryCount}/${MAX_RETRIES} in ${Math.round(delayMs / 1000)}s\n`);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (userRequestedStop || !lastSettings) return;
      const r = spawnJoiner(lastSettings);
      if (!r.ok) send(IPC.LOG, `[main] auto-reconnect spawn failed: ${r.error}\n`);
    }, delayMs);
  });
  return { ok: true };
}

function switchToWorkSession(session: ServiceSessionReady) {
  if (!lastSettings || !lastSettings.serviceControl) return;
  const workSettings: JoinerSettings = {
    ...lastSettings,
    platform: detectPlatformFromLink(session.joinLink),
    link: session.joinLink,
    egressId: session.egressId,
    serviceControl: false,
  };
  userRequestedStop = true;
  switchingToWorkSession = true;
  stopJoiner();
  userRequestedStop = false;
  lastSettings = workSettings;
  setTimeout(() => {
    switchingToWorkSession = false;
    if (joinerProcess) return;
    const result = spawnJoiner(workSettings);
    if (!result.ok) {
      send(IPC.LOG, `[main] work-call start failed: ${result.error}\n`);
      send(IPC.STATUS, 'stopped');
    }
  }, 500);
}

ipcMain.handle(IPC.START, async (_e, settings: JoinerSettings) => {
  if (joinerProcess) {
    return { ok: false, error: 'joiner already running' };
  }
  userRequestedStop = false;
  retryCount = 0;
  lastSettings = settings;
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  return spawnJoiner(settings);
});

ipcMain.handle(IPC.SERVICE_AUTH_STATUS, async () => ({
  authenticated: hasServiceCookies(),
  clientId: serviceUserId(),
}));

ipcMain.handle(IPC.SERVICE_AUTH_LOGIN, async () => openYandexLogin());

ipcMain.handle(IPC.SERVICE_AUTH_CLEAR, async () => {
  if (yandexLoginWindow && !yandexLoginWindow.isDestroyed()) yandexLoginWindow.close();
  await yandexSession().clearStorageData({ storages: ['cookies'] });
  rmSync(serviceCookieFile(), { force: true });
  return { ok: true };
});

ipcMain.handle(IPC.STOP, async () => {
  userRequestedStop = true;
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  stopJoiner();
  return { ok: true };
});

function stopJoiner() {
  closeCaptchaWindow();
  if (!joinerProcess) return;
  // On Linux when the Go binary was spawned via pkexec, it runs as
  // root and we (the user) cannot SIGTERM it. The binary watches
  // stdin: writing "QUIT\n" and closing the pipe triggers the same
  // shutdown path as SIGTERM.
  try { joinerProcess.stdin?.write('QUIT\n'); } catch {}
  try { joinerProcess.stdin?.end(); } catch {}
  try {
    joinerProcess.kill('SIGTERM');
  } catch {}
}
