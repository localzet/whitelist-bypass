type JoinerPlatform = 'wbstream' | 'telemost' | 'vk' | 'dion';

function detectPlatform(url: string): JoinerPlatform | null {
  const u = url.toLowerCase();
  if (!u) return null;
  if (u.includes('wbstream://') || u.includes('stream.wb.ru')) return 'wbstream';
  if (u.includes('telemost.yandex')) return 'telemost';
  if (u.includes('dion://') || u.includes('dion.vc')) return 'dion';
  return 'vk';
}

function platformLabel(p: JoinerPlatform | null): string {
  switch (p) {
    case 'wbstream': return 'WB Stream';
    case 'telemost': return 'Telemost';
    case 'vk': return 'VK';
    case 'dion': return 'DION';
    default: return '-';
  }
}

interface Bridge {
  start(settings: any): Promise<{ ok: boolean; error?: string }>;
  stop(): Promise<{ ok: boolean }>;
  onLog(cb: (text: string) => void): void;
  onStatus(cb: (status: string) => void): void;
  onRunning(cb: (running: boolean) => void): void;
  onEgressList(cb: (egresses: EgressDescriptor[]) => void): void;
  onEgressProbe(cb: (result: EgressProbeResult) => void): void;
}

interface EgressDescriptor { id: string; isDefault: boolean }
interface EgressProbeResult { id: string; available: boolean; latencyMs: number; error?: string }
declare const bridge: Bridge;

const $ = (id: string) => document.getElementById(id) as HTMLElement;
const input = (id: string) => document.getElementById(id) as HTMLInputElement;
const select = (id: string) => document.getElementById(id) as HTMLSelectElement;

const logEl = $('log') as HTMLPreElement;
const statusEl = $('status');
const startBtn = $('start') as HTMLButtonElement;
const stopBtn = $('stop') as HTMLButtonElement;
const downloadLogsBtn = $('downloadLogs') as HTMLImageElement;
const platformHint = $('platformHint');
const egressStatus = $('egressStatus');
const egressOptions = $('egressOptions') as HTMLDataListElement;
let discoveredEgresses: EgressDescriptor[] = [];
const egressProbes = new Map<string, EgressProbeResult>();
const linkInput = input('link');
const savedSettingsRaw = localStorage.getItem('joiner:lastSettings');

if (savedSettingsRaw) {
  try {
    const saved = JSON.parse(savedSettingsRaw);
    input('link').value = saved.link ?? '';
    input('name').value = saved.displayName ?? 'Joiner';
    input('egressId').value = saved.egressId ?? '';
    input('socksPort').value = String(saved.socksPort ?? 1080);
    input('socksUser').value = saved.socksUser ?? '';
    input('socksPass').value = saved.socksPass ?? '';
    select('tunnelMode').value = saved.tunnelMode ?? 'video';
    input('vp8Fps').value = String(saved.vp8Fps ?? 24);
    input('vp8Batch').value = String(saved.vp8Batch ?? 30);
    select('resources').value = saved.resources ?? 'default';
    input('dns').value = saved.dns ?? '1.1.1.1,8.8.8.8';
    input('noTun').checked = Boolean(saved.noTun);
    input('dualTrack').checked = Boolean(saved.dualTrack);
    input('serviceControl').checked = Boolean(saved.serviceControl);
    input('serviceUserId').value = saved.serviceUserId ?? '';
    input('serviceCookieFile').value = saved.serviceCookieFile ?? '';
    select('serviceCookiePlatform').value = saved.serviceCookiePlatform ?? 'telemost';
    select('serviceWorkPlatform').value = saved.serviceWorkPlatform ?? 'telemost';
  } catch {
    localStorage.removeItem('joiner:lastSettings');
  }
}

stopBtn.disabled = true;

downloadLogsBtn.addEventListener('click', () => {
  const blob = new Blob([logEl.textContent || ''], { type: 'text/plain' });
  const anchor = document.createElement('a');
  anchor.href = URL.createObjectURL(blob);
  anchor.download = 'joiner-logs-' + new Date().toISOString().replace(/[:.]/g, '-') + '.txt';
  anchor.click();
  URL.revokeObjectURL(anchor.href);
});

function refreshPlatformHint() {
  const p = detectPlatform(linkInput.value.trim());
  platformHint.textContent = `Detected platform: ${platformLabel(p)}`;
  platformHint.dataset.detected = p ?? '';
}
linkInput.addEventListener('input', refreshPlatformHint);
refreshPlatformHint();

function appendLog(text: string) {
  logEl.textContent += text;
  logEl.scrollTop = logEl.scrollHeight;
}

bridge.onLog((text) => appendLog(text));
bridge.onStatus((s) => {
  statusEl.textContent = s;
  statusEl.dataset.state = s;
});
bridge.onRunning((running) => {
  startBtn.disabled = running;
  stopBtn.disabled = !running;
});
bridge.onEgressList((egresses) => {
  discoveredEgresses = egresses;
  egressProbes.clear();
  renderEgresses();
});
bridge.onEgressProbe((result) => {
  egressProbes.set(result.id, result);
  renderEgresses();
});

function renderEgresses() {
  egressOptions.replaceChildren(...discoveredEgresses.map((egress) => {
    const option = document.createElement('option');
    option.value = egress.id;
    const probe = egressProbes.get(egress.id);
    const state = probe ? (probe.available ? `${probe.latencyMs} ms` : 'unavailable') : 'probing...';
    option.label = `${egress.id}${egress.isDefault ? ' (default)' : ''} — ${state}`;
    return option;
  }));
  if (discoveredEgresses.length === 0) {
    egressStatus.textContent = 'Available profiles will be loaded after connection';
    return;
  }
  egressStatus.textContent = discoveredEgresses.map((egress) => {
    const probe = egressProbes.get(egress.id);
    const state = probe ? (probe.available ? `${probe.latencyMs} ms` : 'offline') : '...';
    return `${egress.id}${egress.isDefault ? '*' : ''}: ${state}`;
  }).join(' · ');
}

startBtn.addEventListener('click', async () => {
  appendLog('\n[ui] starting joiner...\n');
  const link = linkInput.value.trim();
  if (!link) {
    appendLog('[ui] link is required\n');
    return;
  }
  const platform = detectPlatform(link);
  if (!platform) {
    appendLog('[ui] link does not look like a WB Stream or Telemost call\n');
    return;
  }
  const settings = {
    platform,
    link,
    displayName: input('name').value.trim() || 'Joiner',
    socksPort: parseInt(input('socksPort').value, 10) || 1080,
    socksUser: input('socksUser').value,
    socksPass: input('socksPass').value,
    egressId: input('egressId').value.trim(),
    tunnelMode: select('tunnelMode').value,
    vp8Fps: parseInt(input('vp8Fps').value, 10) || 24,
    vp8Batch: parseInt(input('vp8Batch').value, 10) || 30,
    resources: select('resources').value,
    dns: input('dns').value.trim() || '1.1.1.1,8.8.8.8',
    noTun: input('noTun').checked,
    dualTrack: input('dualTrack').checked,
    serviceControl: input('serviceControl').checked,
    serviceUserId: input('serviceUserId').value.trim(),
    serviceCookieFile: input('serviceCookieFile').value.trim(),
    serviceCookiePlatform: select('serviceCookiePlatform').value,
    serviceWorkPlatform: select('serviceWorkPlatform').value,
  };
  localStorage.setItem('joiner:lastSettings', JSON.stringify(settings));
  const r = await bridge.start(settings);
  if (!r.ok) appendLog(`[ui] start failed: ${r.error}\n`);
});

stopBtn.addEventListener('click', async () => {
  appendLog('\n[ui] stopping joiner...\n');
  await bridge.stop();
});
