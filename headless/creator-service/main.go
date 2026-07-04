package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"vconnect/relay/common"
	"vconnect/relay/controlplane"
	"vconnect/relay/egress"
	"vconnect/relay/tunnel"
	"vconnect/relay/wbstream"
)

func main() {
	common.MaybePrintVersion()
	userID := flag.String("user-id", "", "stable user id for this service call")
	userIDs := flag.String("user-ids", "", "comma-separated allowlist of stable user ids")
	serviceCookies := flag.String("service-cookies", "", "path to WB Stream cookies for the bootstrap service call")
	serviceRoom := flag.String("service-room", "", "existing WB Stream service room id/link to rejoin")
	displayName := flag.String("name", "Creator Service", "display name in the service room")
	writeFile := flag.String("write-file", "", "path where the service join link is appended")
	vaultDir := flag.String("vault-dir", "", "directory for encrypted user cookie vault")
	vaultKey := flag.String("vault-key-base64", "", "base64 encoded 32-byte cookie vault key")
	binsDir := flag.String("bins-dir", "", "directory containing headless creator binaries")
	sessionsDir := flag.String("sessions-dir", "", "directory for spawned work creator state/logs")
	egressConfig := flag.String("egress-config", "", "JSON egress profile config")
	resources := flag.String("resources", "default", "resource mode: default, moderate, unlimited, custom")
	workPlatform := flag.String("work-platform", controlplane.PlatformTelemost, "default work call platform")
	customReadBuf := flag.Int("read-buf", 0, "DC read buffer size in bytes, used with -resources custom")
	customMemLimit := flag.Int64("mem-limit", 0, "memory limit in bytes, used with -resources custom")
	maxActiveUsers := flag.Int("max-active-users", 128, "maximum users with active work calls")
	workTTL := flag.Duration("work-ttl", 30*time.Minute, "work call lifetime")
	flag.Parse()

	allowedUsers := parseAllowedUsers(*userID, *userIDs)
	if len(allowedUsers) == 0 || *serviceCookies == "" || *vaultDir == "" || *vaultKey == "" || *binsDir == "" {
		log.Fatal("--user-id or --user-ids, --service-cookies, --vault-dir, --vault-key-base64 and --bins-dir are required")
	}
	readBuf, memLimit := resourceLimits(*resources, *customReadBuf, *customMemLimit)
	if memLimit > 0 {
		debug.SetMemoryLimit(memLimit)
	}
	log.Printf("[config] allowed-users=%d resources=%s read-buf=%d mem-limit=%d", len(allowedUsers), *resources, readBuf, memLimit)

	key, err := controlplane.CookieVaultKeyFromBase64(*vaultKey)
	if err != nil {
		log.Fatalf("[config] vault key: %v", err)
	}
	vault, err := controlplane.NewCookieVault(*vaultDir, key)
	if err != nil {
		log.Fatalf("[config] cookie vault: %v", err)
	}
	registry := loadEgressRegistry(*egressConfig)
	workFactory, err := controlplane.NewProcessWorkCallFactory(controlplane.ProcessFactoryConfig{
		BinsDir:         *binsDir,
		SessionsDir:     *sessionsDir,
		Resources:       *resources,
		EgressConfig:    *egressConfig,
		Cookies:         vault,
		DefaultPlatform: *workPlatform,
	})
	if err != nil {
		log.Fatalf("[config] work factory: %v", err)
	}
	if *maxActiveUsers <= 0 || *workTTL <= 0 {
		log.Fatal("--max-active-users and --work-ttl must be positive")
	}
	manager := controlplane.NewManager(controlplane.Config{MaxUsers: *maxActiveUsers, WorkTTL: *workTTL})
	orchestrator, err := controlplane.NewOrchestrator(manager, registry, workFactory, *workTTL)
	if err != nil {
		log.Fatalf("[config] orchestrator: %v", err)
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := orchestrator.CleanupExpired(context.Background()); err != nil {
				log.Printf("[service] cleanup expired work calls: %v", err)
			}
		}
	}()
	handler := controlplane.ServiceHandler{
		AllowedUserIDs: allowedUsers,
		CookieVault:    vault,
		Sessions:       orchestrator,
	}

	roomID, roomToken, accessToken, serverURL := createOrJoinServiceRoom(*serviceCookies, *serviceRoom, *displayName)
	joinLink := "wbstream://" + roomID
	if *writeFile != "" {
		appendLine(*writeFile, joinLink)
	}

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(roomID))
	if err != nil {
		log.Fatalf("[service] obfuscator init failed: %v", err)
	}
	log.Printf("[service] obfuscator localEpoch=0x%08x", obf.LocalEpoch())
	fmt.Println("")
	fmt.Println("  SERVICE CALL CREATED")
	fmt.Println("  join_link: " + joinLink)
	fmt.Println("")

	var activeBridge *tunnel.RelayBridge
	for {
		sess := wbstream.NewSession(wbstream.SessionConfig{
			RoomToken:   roomToken,
			ServerURL:   serverURL,
			DisplayName: *displayName,
			Obfuscator:  obf,
			LogFn:       log.Printf,
			RoomID:      roomID,
			AccessToken: accessToken,
			ReadBuf:     readBuf,
		})
		sess.OnConnected = func(tun tunnel.DataTunnel) {
			if activeBridge != nil {
				activeBridge.Reset()
			}
			bridgeReadBuf := common.VP8BufSize
			if _, ok := tun.(*tunnel.DCTunnel); ok {
				bridgeReadBuf = readBuf
			}
			activeBridge = tunnel.NewRelayBridge(tun, "creator", bridgeReadBuf, log.Printf)
			activeBridge.SetEgressRegistry(registry)
			if err := handler.BindBridge(context.Background(), activeBridge); err != nil {
				log.Printf("[service] bind bridge failed: %v", err)
			}
			activeBridge.SetOnPeerConfig(func(fps, batch, trackCount int) {
				sess.AdaptTrackCount(trackCount)
			})
			fmt.Println("")
			fmt.Println("  SERVICE TUNNEL CONNECTED")
			fmt.Println("")
		}
		sess.OnPeerRestart = func() {
			if activeBridge != nil {
				log.Printf("[service] peer restarted, resetting service bridge")
				activeBridge.Reset()
			}
		}
		if err := sess.Start(); err != nil {
			log.Printf("[service] start failed: %v, retrying in 5s", err)
			sess.Close()
			time.Sleep(5 * time.Second)
		} else {
			<-sess.Done()
			log.Printf("[service] session ended, rejoining in 3s")
			sess.Close()
		}
		if activeBridge != nil {
			activeBridge.Reset()
		}
		time.Sleep(3 * time.Second)
		roomToken, accessToken, serverURL = refreshServiceRoom(*serviceCookies, roomID, *displayName)
	}
}

func parseAllowedUsers(single, csv string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, value := range append([]string{single}, strings.Split(csv, ",")...) {
		if userID := strings.TrimSpace(value); userID != "" {
			allowed[userID] = struct{}{}
		}
	}
	return allowed
}

func createOrJoinServiceRoom(cookiesPath, roomFlag, displayName string) (roomID, roomToken, accessToken, serverURL string) {
	cookieHeader, deviceID := loadWBCookies(cookiesPath)
	bearer, err := wbstream.RefreshAccessToken(nil, cookieHeader, deviceID)
	if err != nil {
		log.Fatalf("[service] refresh token: %v", err)
	}
	requestedRoom := wbstream.ParseRoomID(roomFlag)
	roomID, roomToken, accessToken, serverURL, err = wbstream.AuthAsLoggedIn(nil, cookieHeader, bearer, requestedRoom, displayName)
	if err != nil {
		log.Fatalf("[service] auth room: %v", err)
	}
	log.Printf("[service] room=%s server=%s", roomID, serverURL)
	return roomID, roomToken, accessToken, serverURL
}

func refreshServiceRoom(cookiesPath, roomID, displayName string) (roomToken, accessToken, serverURL string) {
	cookieHeader, deviceID := loadWBCookies(cookiesPath)
	bearer, err := wbstream.RefreshAccessToken(nil, cookieHeader, deviceID)
	if err != nil {
		log.Printf("[service] refresh token failed: %v", err)
		time.Sleep(5 * time.Second)
		return refreshServiceRoom(cookiesPath, roomID, displayName)
	}
	_, roomToken, accessToken, serverURL, err = wbstream.AuthAsLoggedIn(nil, cookieHeader, bearer, roomID, displayName)
	if err != nil {
		log.Printf("[service] reauth failed: %v", err)
		time.Sleep(5 * time.Second)
		return refreshServiceRoom(cookiesPath, roomID, displayName)
	}
	return roomToken, accessToken, serverURL
}

func loadWBCookies(cookiesPath string) (cookieHeader, deviceID string) {
	rawCookies := common.LoadCookies(cookiesPath)
	deviceID = common.CookieValue(rawCookies, "__wb_device_id")
	if deviceID == "" {
		log.Fatalf("[service] cookies file is missing __wb_device_id")
	}
	return common.FilterCookies(rawCookies, wbstream.WBStreamCookieAllowlist), deviceID
}

func resourceLimits(resources string, customReadBuf int, customMemLimit int64) (int, int64) {
	switch resources {
	case "moderate":
		return 16384, 64 << 20
	case "default":
		return common.DCBufSize, 128 << 20
	case "unlimited":
		return common.RTPBufSize, 256 << 20
	case "custom":
		readBuf := customReadBuf
		if readBuf == 0 {
			readBuf = common.RTPBufSize
		}
		memLimit := customMemLimit
		if memLimit == 0 {
			memLimit = 256 << 20
		}
		return readBuf, memLimit
	default:
		log.Fatalf("[config] unknown resources mode: %s", resources)
		return 0, 0
	}
}

func loadEgressRegistry(configPath string) *egress.Registry {
	if configPath == "" {
		return egress.DirectRegistry()
	}
	reg, err := egress.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("[config] egress config: %v", err)
	}
	log.Printf("[config] loaded egress config from %s", configPath)
	return reg
}

func appendLine(path, line string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("[config] open write-file: %v", err)
	}
	fmt.Fprintln(file, line)
	file.Close()
	log.Printf("[config] wrote service join link to %s", path)
}
