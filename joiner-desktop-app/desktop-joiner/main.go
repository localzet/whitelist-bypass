// Command desktop-joiner is the engine behind the desktop joiner GUI.
// On Windows it brings up a wintun adapter so every IP packet on the
// host is steered through the resulting SOCKS5 proxy. On Linux it
// only exposes the SOCKS5 proxy (TUN routing is left to the user).
//
// On Windows it must run with administrator rights (the embedded
// manifest asks for them); creating wintun adapters and editing the
// route table both require elevation.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"whitelist-bypass/relay/common"
	"whitelist-bypass/relay/desktoptun"
	"whitelist-bypass/relay/dion"
	"whitelist-bypass/relay/pion"
	joinerCommon "whitelist-bypass/relay/pion/headless-joiner-common"
	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"
)

type statusEmitter struct{}

var tunnelLostCh = make(chan struct{}, 1)
var selfHealReconnect bool

func (statusEmitter) EmitStatus(status string) {
	log.Printf("[status] %s", status)
	// CAPTCHA:url is fired by the VK auth path when an interactive
	// captcha is required. The Electron wrapper watches stdout for
	// this exact prefix and opens a BrowserWindow at the URL.
	if strings.HasPrefix(status, "CAPTCHA:") {
		fmt.Printf("STATUS:%s\n", status)
	}
	if status == common.StatusTunnelLost && !selfHealReconnect {
		select {
		case tunnelLostCh <- struct{}{}:
		default:
		}
	}
}
func (statusEmitter) EmitStatusError(msg string) {
	log.Printf("[status] ERROR: %s", msg)
	select {
	case tunnelLostCh <- struct{}{}:
	default:
	}
}

type fileCacheStore struct{ dir string }

func newFileCacheStore() *fileCacheStore {
	dir, _ := os.UserCacheDir()
	if dir == "" {
		dir = os.TempDir()
	}
	cacheDir := filepath.Join(dir, "whitelist-bypass")
	os.MkdirAll(cacheDir, 0755)
	return &fileCacheStore{dir: cacheDir}
}

func (c *fileCacheStore) Save(key, value string) {
	os.WriteFile(filepath.Join(c.dir, key), []byte(value), 0644)
}

func (c *fileCacheStore) Load(key string) string {
	data, err := os.ReadFile(filepath.Join(c.dir, key))
	if err != nil {
		return ""
	}
	return string(data)
}

const (
	tunAdapter = "WhitelistBypass"
	tunIP      = "10.99.0.2"
	tunMask    = "255.255.255.0"
	tunPeer    = "10.99.0.1"
	tunMTU     = 1500
)

func main() {
	platform := flag.String("platform", "", "wbstream | telemost | vk | dion (required)")
	link := flag.String("link", "", "WB Stream room link, Telemost join URI, VK call link, or DION event link (required)")
	displayName := flag.String("name", "Joiner", "display name in the room")
	socksHost := flag.String("socks-host", common.SocksLocalhostIP, "SOCKS5 listen address (use 0.0.0.0 to expose on LAN; tun2socks always connects via loopback)")
	socksPort := flag.Int("socks-port", 1080, "local SOCKS5 port")
	socksUser := flag.String("socks-user", "", "optional SOCKS5 username")
	socksPass := flag.String("socks-pass", "", "optional SOCKS5 password")
	egressID := flag.String("egress-id", "", "creator egress profile id")
	serviceControl := flag.Bool("service-control", false, "use this call as a control channel and request a work call")
	serviceUserID := flag.String("service-user-id", "", "user id sent in service control messages")
	serviceCookieFile := flag.String("service-cookie-file", "", "optional cookies JSON file to send through the service call")
	serviceCookiePlatform := flag.String("service-cookie-platform", "telemost", "platform for service cookie submit")
	serviceWorkPlatform := flag.String("service-work-platform", "telemost", "platform requested for the work call")
	serviceRequestID := flag.String("service-request-id", "", "idempotency key for the service session request")
	resources := flag.String("resources", "default", "moderate | default | unlimited")
	tunnelMode := flag.String("tunnel-mode", "video", "tunnel mode for WB Stream: video | dc")
	vp8FPS := flag.Int("vp8-fps", 24, "VP8 frame rate")
	vp8Batch := flag.Int("vp8-batch", 30, "VP8 batch multiplier")
	dns := flag.String("dns", "1.1.1.1,8.8.8.8", "comma-separated DNS servers for the tunnel adapter")
	noTun := flag.Bool("no-tun", false, "expose SOCKS5 only, do not bring up the wintun adapter")
	dualTrack := flag.Bool("dual-track", false, "VK/WB Stream: dual-track tunnel (second screenshare channel) for higher throughput")
	flag.Parse()

	if *platform == "" || *link == "" {
		log.Fatal("--platform and --link are required")
	}
	if *serviceControl && *serviceRequestID == "" {
		*serviceRequestID = newRequestID()
	}

	switch *resources {
	case "moderate":
		debug.SetMemoryLimit(64 << 20)
	case "default":
		debug.SetMemoryLimit(128 << 20)
	case "unlimited":
		debug.SetMemoryLimit(256 << 20)
	default:
		log.Fatalf("[config] unknown resources mode: %s", *resources)
	}

	// One desktoptun.Tunnel covers both platforms. Created up-front so
	// signaling-host bypass routes can be installed before any platform
	// code touches the network.
	var tun *desktoptun.Tunnel
	if !*noTun {
		cfg := desktoptun.Config{
			AdapterName: tunAdapter,
			TunnelIP:    tunIP,
			TunnelMask:  tunMask,
			TunnelPeer:  tunPeer,
			MTU:         tunMTU,
			DNSServers:  splitCSV(*dns),
			SocksHost:   common.SocksLocalhostIP,
			SocksPort:   *socksPort,
			SocksUser:   *socksUser,
			SocksPass:   *socksPass,
			LogFn:       log.Printf,
		}
		var err error
		tun, err = desktoptun.New(cfg)
		if err != nil {
			log.Fatalf("[desktoptun] init: %v", err)
		}
	}

	// Add bypass routes for the signaling hosts before any traffic
	// from the joiner reaches them. These are needed even before
	// engine.Start, because the joiner opens its WebSocket as soon
	// as we call Start() below.
	bypassHosts := signalingHosts(*platform, *link)
	preResolved := map[string][]net.IP{}
	for _, h := range bypassHosts {
		ips, err := net.LookupIP(h)
		if err != nil {
			log.Printf("[bypass] resolve %s: %v (will rely on candidate hook)", h, err)
			continue
		}
		preResolved[h] = ips
		log.Printf("[bypass] %s -> %v (pre-tun)", h, ips)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	watchStdinQuit(sig)

	tunReady := make(chan struct{})
	var tunOnce sync.Once
	var (
		pendingMu  sync.Mutex
		pending    []string
		tunStarted bool
	)
	bringUpTun := func() {
		tunOnce.Do(func() {
			if tun == nil {
				close(tunReady)
				return
			}
			if err := tun.Start(); err != nil {
				log.Fatalf("[desktoptun] start: %v", err)
			}
			for host, ips := range preResolved {
				for _, ip := range ips {
					if err := tun.AddBypassIP(ip); err != nil {
						log.Printf("[bypass] %s ip %s: %v", host, ip, err)
					}
				}
			}
			pendingMu.Lock()
			drained := pending
			pending = nil
			tunStarted = true
			pendingMu.Unlock()
			for _, c := range drained {
				if err := tun.AddBypassFromCandidate(c); err != nil {
					log.Printf("[bypass] replay: %v", err)
				}
			}
			fmt.Printf("\n  TUNNEL ACTIVE on adapter %q (DNS=%s)\n  all traffic now egresses via %s\n\n",
				tunAdapter, *dns, *platform)
			close(tunReady)
		})
	}

	tryBypass := func(c string) {
		if err := tun.AddBypassFromCandidate(c); err != nil {
			pendingMu.Lock()
			if !tunStarted {
				pending = append(pending, c)
				pendingMu.Unlock()
				return
			}
			pendingMu.Unlock()
			log.Printf("[bypass] candidate: %v", err)
		}
	}

	addCandidate := func(target int, candidateOrSDP string) {
		if tun == nil {
			return
		}
		tryBypass(candidateOrSDP)
		if strings.Contains(candidateOrSDP, "a=candidate:") {
			for _, line := range strings.Split(candidateOrSDP, "\n") {
				line = strings.TrimRight(line, "\r")
				if strings.HasPrefix(line, "a=candidate:") {
					tryBypass(line)
				}
			}
		}
	}

	var (
		bridge   *tunnel.RelayBridge
		bridgeMu sync.Mutex
	)
	serviceReadyCh := make(chan tunnel.SessionReady, 1)
	serviceErrorCh := make(chan string, 1)
	serviceCookieAckCh := make(chan tunnel.CookieAck, 1)
	serviceRuntime := newServiceControlRuntime()
	onConnected := func(t tunnel.DataTunnel) {
		readBuf := common.VP8BufSize
		if _, ok := t.(*tunnel.DCTunnel); ok {
			readBuf = common.DCBufSize
		}
		bridgeMu.Lock()
		defer bridgeMu.Unlock()
		if *serviceControl {
			if bridge != nil {
				bridge.SwapTunnel(t)
				log.Printf("[service] control tunnel swapped after reconnect")
				return
			}
			bridge = tunnel.NewRelayBridge(t, "joiner", readBuf, log.Printf)
			bridge.SetRequestedEgressID("")
			bridge.SetOnCookieAck(func(ack tunnel.CookieAck) {
				log.Printf("[service] cookie stored platform=%q request=%q", ack.Platform, ack.RequestID)
				serviceRuntime.markCookieAcked()
				select {
				case serviceCookieAckCh <- ack:
				default:
				}
			})
			bridge.SetOnControlError(func(controlErr tunnel.ControlError) {
				log.Printf("[service] control error %s: %s", controlErr.Code, controlErr.SafeMessage)
				if isTerminalServiceError(controlErr) {
					serviceRuntime.markReady()
					fmt.Printf("STATUS:ERROR:%s\n", controlErr.SafeMessage)
					select {
					case serviceErrorCh <- controlErr.SafeMessage:
					default:
					}
				}
			})
			bridge.SetOnSessionReady(func(session tunnel.SessionReady) {
				payload, _ := json.Marshal(session)
				fmt.Printf("SERVICE_SESSION_READY:%s\n", payload)
				serviceRuntime.markReady()
				select {
				case serviceReadyCh <- session:
				default:
				}
			})
			bridge.MarkReady()
			serviceRuntime.startRequestLoop(bridge, serviceControlConfig{
				UserID:         *serviceUserID,
				RequestID:      *serviceRequestID,
				EgressID:       *egressID,
				CookieFile:     *serviceCookieFile,
				CookiePlatform: *serviceCookiePlatform,
				WorkPlatform:   *serviceWorkPlatform,
				TunnelMode:     *tunnelMode,
			})
			log.Printf("[service] control channel ready request=%q", *serviceRequestID)
			return
		}

		// Reconnect: swap the new tunnel behind the persistent SOCKS
		// listener instead of binding a second one
		if bridge != nil {
			bridge.SwapTunnel(t)
			log.Printf("[socks] tunnel swapped after reconnect")
			return
		}
		bridge = tunnel.NewRelayBridgeWithAuth(t, "joiner", readBuf, log.Printf, *socksUser, *socksPass)
		bridge.SetRequestedEgressID(*egressID)
		bridge.SetPersistentListener(true)
		bridge.MarkReady()
		addr := fmt.Sprintf("%s:%d", *socksHost, *socksPort)
		go func() {
			if err := bridge.ListenSOCKS(addr); err != nil {
				log.Printf("[socks] listen: %v", err)
			}
		}()
		log.Printf("[socks] listening on %s", addr)
		// SOCKS5 is up; bring up wintun so the OS starts steering
		// traffic into it. Doing this after the joiner has connected
		// also means we already have remote candidates and bypass
		// routes are in place.
		bringUpTun()
	}

	switch strings.ToLower(*platform) {
	case "wbstream", "wb":
		runWBStream(*link, *displayName, *tunnelMode, *vp8FPS, *vp8Batch, *dualTrack,
			onConnected, addCandidate)
	case "telemost", "tm":
		runTelemost(*link, *displayName, *vp8FPS, *vp8Batch,
			onConnected, addCandidate)
	case "vk":
		selfHealReconnect = true
		runVK(*link, *displayName, *tunnelMode, *vp8FPS, *vp8Batch, *dualTrack,
			onConnected, addCandidate)
	case "dion", "dn":
		runDion(*link, *displayName, onConnected, addCandidate)
	default:
		log.Fatalf("[config] unknown --platform %q", *platform)
	}

	var lost bool
	var failed bool
	select {
	case <-sig:
		log.Printf("[main] shutting down")
	case session := <-serviceReadyCh:
		log.Printf("[service] work session ready id=%s egress=%s", session.SessionID, session.EgressID)
	case msg := <-serviceErrorCh:
		log.Printf("[service] failed: %s", msg)
		failed = true
	case <-tunnelLostCh:
		log.Printf("[main] tunnel lost, exiting with code 2 to trigger auto-reconnect")
		lost = true
	}
	if tun != nil {
		tun.Stop()
	}
	// Give in-flight goroutines a beat to drain before the process exits.
	time.Sleep(200 * time.Millisecond)
	if lost {
		os.Exit(2)
	}
	if failed {
		os.Exit(1)
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func signalingHosts(platform, link string) []string {
	switch strings.ToLower(platform) {
	case "wbstream", "wb":
		return []string{"stream.wb.ru", "rtc-el-01.wb.ru"}
	case "telemost", "tm":
		hosts := []string{"telemost.yandex.ru", "telemost-api.yandex.ru"}
		if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
		return hosts
	case "vk":
		hosts := []string{"vk.com", "login.vk.com", "api.vk.com", "ok.ru", "cloud-api.yandex.ru"}
		if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
		return hosts
	case "dion", "dn":
		return []string{"dion.vc", "api.dion.vc", "api-clients.dion.vc"}
	}
	return nil
}

func runWBStream(link, name, mode string, fps, batch int, dualTrack bool,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	id := wbstream.ParseRoomID(link)
	roomID, roomToken, _, serverURL, err := wbstream.AuthAndGetToken(nil, id, name)
	if err != nil {
		log.Fatalf("[wb] auth: %v", err)
	}
	log.Printf("[wb] room=%s server=%s mode=%s", roomID, serverURL, mode)

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(roomID))
	if err != nil {
		log.Fatalf("[wb] obfuscator: %v", err)
	}

	sess := wbstream.NewSession(wbstream.SessionConfig{
		RoomToken:   roomToken,
		ServerURL:   serverURL,
		DisplayName: name,
		TunnelMode:  mode,
		Obfuscator:  obf,
		LogFn:       log.Printf,
		VP8FPS:      fps,
		VP8Batch:    batch,
		ScreenShare: dualTrack,
	})
	sess.OnConnected = onConnected
	sess.OnRemoteCandidate = onCandidate

	if err := sess.Start(); err != nil {
		log.Fatalf("[wb] session: %v", err)
	}
}

func runTelemost(link, name string, fps, batch int,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	inner := joinerCommon.NewTelemostHeadlessJoiner(
		log.Printf,
		resolveHostname,
		statusEmitter{},
		nil,
		pion.AddTunnelTracks,
		pion.ReadTrack,
	)
	inner.OnConnected = onConnected
	inner.OnRemoteCandidate = onCandidate

	params, _ := json.Marshal(struct {
		JoinLink    string `json:"joinLink"`
		DisplayName string `json:"displayName"`
		VP8FPS      int    `json:"vp8Fps"`
		VP8Batch    int    `json:"vp8Batch"`
	}{
		JoinLink:    strings.TrimSpace(link),
		DisplayName: name,
		VP8FPS:      fps,
		VP8Batch:    batch,
	})
	go inner.RunWithParams(string(params))
}

func runVK(link, name, mode string, fps, batch int, dualTrack bool,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	emitter := statusEmitter{}
	statusFn := func(s string) { emitter.EmitStatus(s) }

	authJSON, err := joinerCommon.RunVKAuth(strings.TrimSpace(link), name,
		log.Printf, statusFn, newFileCacheStore(), resolveHostname)
	if err != nil {
		log.Fatalf("[vk] auth: %v", err)
	}

	var authParams map[string]interface{}
	if json.Unmarshal([]byte(authJSON), &authParams) != nil {
		log.Fatalf("[vk] auth response not JSON: %s", authJSON)
	}
	authParams["tunnelMode"] = mode
	authParams["vp8Fps"] = fps
	authParams["vp8Batch"] = batch
	authParams["dualTrack"] = dualTrack
	patched, err := json.Marshal(authParams)
	if err != nil {
		log.Fatalf("[vk] auth marshal: %v", err)
	}

	inner := joinerCommon.NewVKHeadlessJoiner(
		log.Printf,
		resolveHostname,
		emitter,
		nil,
		pion.AddTunnelTracks,
		pion.ReadTrack,
	)
	inner.OnConnected = onConnected
	inner.OnRemoteCandidate = onCandidate
	go inner.RunWithParams(string(patched))
}

func runDion(link, name string,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	room := dion.ParseRoom(link)
	if room == "" {
		log.Fatalf("[dion] --link must be a room id or https://dion.vc/event/<id>")
	}
	auth, event, err := dion.JoinAsGuest(nil, room, name)
	if err != nil {
		log.Fatalf("[dion] JoinAsGuest: %v", err)
	}
	log.Printf("[dion] room=%s event_id=%s", event.Slug, event.ID)

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(event.Slug))
	if err != nil {
		log.Fatalf("[dion] obfuscator: %v", err)
	}

	call := dion.NewCall(dion.CallConfig{
		Auth:        auth,
		Event:       event,
		Obfuscator:  obf,
		DisplayName: name,
		LogFn:       log.Printf,
		Role:        dion.RoleJoiner,
	})
	call.OnConnected = onConnected
	call.OnRemoteSDP = func(sdp string) { onCandidate(0, sdp) }

	if err := call.Start(); err != nil {
		log.Fatalf("[dion] call.Start: %v", err)
	}
	go func() {
		<-call.Done()
		select {
		case tunnelLostCh <- struct{}{}:
		default:
		}
	}()
}

type serviceControlConfig struct {
	UserID         string
	RequestID      string
	EgressID       string
	CookieFile     string
	CookiePlatform string
	WorkPlatform   string
	TunnelMode     string
}

type serviceControlRuntime struct {
	ready       chan struct{}
	readyOnce   sync.Once
	loopRunning atomic.Bool
	cookieAcked atomic.Bool
}

func newServiceControlRuntime() *serviceControlRuntime {
	return &serviceControlRuntime{ready: make(chan struct{})}
}

func (rt *serviceControlRuntime) markReady() {
	rt.readyOnce.Do(func() { close(rt.ready) })
}

func (rt *serviceControlRuntime) markCookieAcked() {
	rt.cookieAcked.Store(true)
}

func (rt *serviceControlRuntime) startRequestLoop(bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	if !rt.loopRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer rt.loopRunning.Store(false)
		sessionTicker := time.NewTicker(serviceSessionRetryInterval)
		defer sessionTicker.Stop()
		cookieTicker := time.NewTicker(serviceCookieRetryInterval)
		defer cookieTicker.Stop()

		requestTimer := time.NewTimer(serviceSessionRetryInterval)
		defer requestTimer.Stop()
		for {
			select {
			case <-rt.ready:
				return
			case <-requestTimer.C:
				requestServiceSession(bridge, cfg)
			case <-sessionTicker.C:
				requestServiceSessionWithoutCookies(bridge, cfg)
			case <-cookieTicker.C:
				if strings.TrimSpace(cfg.CookieFile) != "" && !rt.cookieAcked.Load() {
					requestServiceSession(bridge, cfg)
				}
			}
		}
	}()
}

const (
	serviceSessionRetryInterval = 2 * time.Second
	serviceCookieRetryInterval  = 10 * time.Second
)

func requestServiceSession(bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	if strings.TrimSpace(cfg.CookieFile) != "" {
		payload, err := os.ReadFile(cfg.CookieFile)
		if err != nil {
			log.Printf("[service] read cookies: %v", err)
		} else {
			bridge.SubmitCookies(tunnel.CookieSubmit{
				RequestID: cfg.RequestID + "-cookies",
				UserID:    cfg.UserID,
				Platform:  cfg.CookiePlatform,
				Format:    "json",
				Payload:   string(payload),
			})
			log.Printf("[service] submitted cookies platform=%q request=%q", cfg.CookiePlatform, cfg.RequestID+"-cookies")
		}
	}
	bridge.RequestSession(tunnel.SessionCreateRequest{
		RequestID: cfg.RequestID,
		UserID:    cfg.UserID,
		EgressID:  cfg.EgressID,
		Platform:  cfg.WorkPlatform,
		Mode:      cfg.TunnelMode,
	})
	log.Printf("[service] requested work session platform=%q egress=%q request=%q", cfg.WorkPlatform, cfg.EgressID, cfg.RequestID)
}

func requestServiceSessionWithoutCookies(bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	bridge.RequestSession(tunnel.SessionCreateRequest{
		RequestID: cfg.RequestID,
		UserID:    cfg.UserID,
		EgressID:  cfg.EgressID,
		Platform:  cfg.WorkPlatform,
		Mode:      cfg.TunnelMode,
	})
	log.Printf("[service] retried work session request platform=%q egress=%q request=%q", cfg.WorkPlatform, cfg.EgressID, cfg.RequestID)
}

func isTerminalServiceError(controlErr tunnel.ControlError) bool {
	switch controlErr.Code {
	case "session_create_failed", "cookie_submit_failed", "bad_session_request", "bad_cookie_submit", "session_control_unavailable", "cookie_vault_unavailable":
		return true
	default:
		return false
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func resolveHostname(hostname string) (string, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		return hostname, nil
	}
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	for _, ip := range ips {
		return ip.String(), nil
	}
	return "", fmt.Errorf("no IPs for %s", hostname)
}
