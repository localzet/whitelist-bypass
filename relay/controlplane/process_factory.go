package controlplane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	PlatformVK       = "vk"
	PlatformTelemost = "telemost"
	PlatformWBStream = "wbstream"
	PlatformDION     = "dion"
)

type CookieResolver interface {
	CookiePath(userID, platform string) (string, error)
}

type StaticCookieResolver map[string]string

func (r StaticCookieResolver) CookiePath(_ string, platform string) (string, error) {
	path := strings.TrimSpace(r[platform])
	if path == "" && platform == PlatformTelemost {
		path = strings.TrimSpace(r["tm"])
	}
	if path == "" {
		return "", fmt.Errorf("controlplane: cookies for platform %q are not configured", platform)
	}
	return path, nil
}

type ProcessFactoryConfig struct {
	BinsDir         string
	SessionsDir     string
	Resources       string
	EgressConfig    string
	Cookies         CookieResolver
	SpawnTimeout    time.Duration
	DefaultPlatform string
}

type ProcessWorkCallFactory struct {
	cfg       ProcessFactoryConfig
	mu        sync.Mutex
	processes map[string]*exec.Cmd
}

func NewProcessWorkCallFactory(cfg ProcessFactoryConfig) (*ProcessWorkCallFactory, error) {
	cfg.BinsDir = strings.TrimSpace(cfg.BinsDir)
	if cfg.BinsDir == "" {
		return nil, errors.New("controlplane: bins dir is required")
	}
	if cfg.SessionsDir == "" {
		cfg.SessionsDir = os.TempDir()
	}
	if cfg.Resources == "" {
		cfg.Resources = "default"
	}
	if cfg.SpawnTimeout <= 0 {
		cfg.SpawnTimeout = 60 * time.Second
	}
	if cfg.DefaultPlatform == "" {
		cfg.DefaultPlatform = PlatformTelemost
	}
	return &ProcessWorkCallFactory{
		cfg:       cfg,
		processes: make(map[string]*exec.Cmd),
	}, nil
}

func (f *ProcessWorkCallFactory) CreateWorkCall(ctx context.Context, request WorkCallRequest) (WorkCall, error) {
	platform := normalizePlatform(request.Platform)
	if platform == "" {
		platform = normalizePlatform(f.cfg.DefaultPlatform)
	}
	binName, err := creatorBinary(platform)
	if err != nil {
		return WorkCall{}, err
	}
	bin, err := resolveBinary(f.cfg.BinsDir, binName)
	if err != nil {
		return WorkCall{}, err
	}
	if err := os.MkdirAll(f.cfg.SessionsDir, 0755); err != nil {
		return WorkCall{}, err
	}
	linkFile, err := os.CreateTemp(f.cfg.SessionsDir, "work-link-*.txt")
	if err != nil {
		return WorkCall{}, err
	}
	linkPath := linkFile.Name()
	linkFile.Close()
	os.Remove(linkPath)

	args := []string{"--write-file", linkPath, "--resources", f.cfg.Resources}
	if platformNeedsCookies(platform) {
		if f.cfg.Cookies == nil {
			return WorkCall{}, fmt.Errorf("controlplane: cookie resolver is required for platform %q", platform)
		}
		cookies, err := f.cfg.Cookies.CookiePath(request.UserID, platform)
		if err != nil {
			return WorkCall{}, err
		}
		args = append(args, "--cookies", cookies)
	}
	if f.cfg.EgressConfig != "" {
		args = append(args, "--egress-config", f.cfg.EgressConfig)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	logPath := filepath.Join(f.cfg.SessionsDir, fmt.Sprintf("work-%s-%s.log", platform, request.RequestID))
	logFile, err := os.Create(logPath)
	if err != nil {
		return WorkCall{}, err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return WorkCall{}, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	link, err := waitForFirstLine(ctx, linkPath, f.cfg.SpawnTimeout, waitCh)
	if err != nil {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-waitCh
		}
		return WorkCall{}, fmt.Errorf("%w (log: %s)", err, logPath)
	}
	if cmd.ProcessState != nil {
		return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link (log: %s)", logPath)
	}
	select {
	case err := <-waitCh:
		if err != nil {
			return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link: %w (log: %s)", err, logPath)
		}
		return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link (log: %s)", logPath)
	default:
	}
	f.mu.Lock()
	f.processes[link] = cmd
	f.mu.Unlock()
	go func() {
		if err := <-waitCh; err != nil {
			// The process may be killed during normal session cleanup.
			return
		}
	}()
	return WorkCall{JoinLink: link}, nil
}

func (f *ProcessWorkCallFactory) CloseWorkCall(_ context.Context, session Session) error {
	f.mu.Lock()
	cmd := f.processes[session.JoinLink]
	delete(f.processes, session.JoinLink)
	f.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func normalizePlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "tm", "telemost", "yandex":
		return PlatformTelemost
	case "vk":
		return PlatformVK
	case "wb", "wbstream":
		return PlatformWBStream
	case "dion":
		return PlatformDION
	default:
		return strings.ToLower(strings.TrimSpace(platform))
	}
}

func creatorBinary(platform string) (string, error) {
	switch platform {
	case PlatformVK:
		return "headless-vk-creator", nil
	case PlatformTelemost:
		return "headless-telemost-creator", nil
	case PlatformWBStream:
		return "headless-wbstream-creator", nil
	case PlatformDION:
		return "headless-dion-creator", nil
	default:
		return "", fmt.Errorf("controlplane: unsupported platform %q", platform)
	}
}

func platformNeedsCookies(platform string) bool {
	switch platform {
	case PlatformWBStream:
		return false
	default:
		return true
	}
}

func resolveBinary(dir, name string) (string, error) {
	exact := filepath.Join(dir, name)
	if info, err := os.Stat(exact); err == nil && !info.IsDir() {
		return exact, nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, name+"*"))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && !info.IsDir() {
			return match, nil
		}
	}
	return "", fmt.Errorf("controlplane: binary %s not found in %s", name, dir)
}

func waitForFirstLine(ctx context.Context, path string, timeout time.Duration, processDone <-chan error) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-processDone:
			if err != nil {
				return "", fmt.Errorf("controlplane: creator exited before writing link: %w", err)
			}
			return "", errors.New("controlplane: creator exited before writing link")
		case <-deadline.C:
			return "", fmt.Errorf("controlplane: creator did not write link within %s", timeout)
		case <-ticker.C:
			line, ok := readFirstLine(path)
			if ok {
				return line, nil
			}
		}
	}
}

func readFirstLine(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return "", false
	}
	line := strings.TrimSpace(scanner.Text())
	return line, line != ""
}
