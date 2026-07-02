package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizePlatform(t *testing.T) {
	tests := map[string]string{
		"tm":       PlatformTelemost,
		"Telemost": PlatformTelemost,
		"yandex":   PlatformTelemost,
		"vk":       PlatformVK,
		"wb":       PlatformWBStream,
		"wbstream": PlatformWBStream,
		"dion":     PlatformDION,
	}
	for input, want := range tests {
		if got := normalizePlatform(input); got != want {
			t.Fatalf("normalizePlatform(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStaticCookieResolver(t *testing.T) {
	resolver := StaticCookieResolver{
		"tm": "/secure/user-1/yandex.json",
	}
	path, err := resolver.CookiePath("user-1", PlatformTelemost)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/secure/user-1/yandex.json" {
		t.Fatalf("CookiePath() = %q", path)
	}
	if _, err := resolver.CookiePath("user-1", PlatformVK); err == nil {
		t.Fatal("CookiePath() expected missing cookies error")
	}
}

func TestResolveBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "headless-telemost-creator-test")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveBinary(dir, "headless-telemost-creator")
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveBinary() = %q, want %q", got, path)
	}
}

func TestCreatorBinaryRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := creatorBinary("unknown"); err == nil {
		t.Fatal("creatorBinary() expected unsupported platform error")
	}
}

func TestProcessFactoryRejectsUnsupportedCookieSource(t *testing.T) {
	if _, err := NewProcessWorkCallFactory(ProcessFactoryConfig{
		BinsDir:      t.TempDir(),
		CookieSource: "unknown",
	}); err == nil {
		t.Fatal("NewProcessWorkCallFactory() expected unsupported cookie source error")
	}
}

func TestProcessFactorySelectsServiceCookieResolver(t *testing.T) {
	factory, err := NewProcessWorkCallFactory(ProcessFactoryConfig{
		BinsDir:        t.TempDir(),
		Cookies:        StaticCookieResolver{PlatformTelemost: "/user-cookies.json"},
		ServiceCookies: StaticCookieResolver{PlatformTelemost: "/service-cookies.json"},
		CookieSource:   "service",
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := factory.cookieResolver().CookiePath("user-1", PlatformTelemost)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/service-cookies.json" {
		t.Fatalf("CookiePath() = %q, want service cookies", path)
	}
}

func TestWaitForFirstLineReturnsWhenCreatorExits(t *testing.T) {
	done := make(chan error, 1)
	done <- os.ErrPermission
	started := time.Now()
	_, err := waitForFirstLine(context.Background(), filepath.Join(t.TempDir(), "missing.txt"), time.Minute, done)
	if err == nil {
		t.Fatal("waitForFirstLine() expected process exit error")
	}
	if time.Since(started) > time.Second {
		t.Fatal("waitForFirstLine() waited for timeout after process exit")
	}
	if !strings.Contains(err.Error(), "creator exited before writing link") {
		t.Fatalf("unexpected error: %v", err)
	}
}
