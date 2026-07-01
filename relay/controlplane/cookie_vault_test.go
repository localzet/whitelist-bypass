package controlplane

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
)

func TestCookieVaultStoresEncryptedUserScopedCookies(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	vault, err := NewCookieVault(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	vault.SetClockForTest(func() time.Time { return now })

	ack, err := vault.StoreSubmit("user-1", tunnel.CookieSubmit{
		RequestID: "cookie-1",
		Platform:  "tm",
		Format:    CookieFormatJSON,
		Payload:   `[{"name":"Session_id","value":"secret"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Stored || ack.Platform != PlatformTelemost {
		t.Fatalf("ack = %+v", ack)
	}

	record, err := vault.Load("user-1", PlatformTelemost)
	if err != nil {
		t.Fatal(err)
	}
	if record.UserID != "user-1" || record.Platform != PlatformTelemost || record.Payload == "" || !record.CreatedAt.Equal(now) {
		t.Fatalf("record = %+v", record)
	}

	encrypted, err := os.ReadFile(filepath.Join(vault.root, "user-1", PlatformTelemost+".cookies.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("secret")) {
		t.Fatal("encrypted cookie record contains plaintext cookie value")
	}
}

func TestCookieVaultCookiePathWritesUserScopedJSON(t *testing.T) {
	vault, err := NewCookieVault(t.TempDir(), bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	const payload = `[{"name":"Session_id","value":"secret"}]`
	if _, err := vault.StoreSubmit("user-1", tunnel.CookieSubmit{
		RequestID: "cookie-1",
		Platform:  PlatformTelemost,
		Format:    CookieFormatJSON,
		Payload:   payload,
	}); err != nil {
		t.Fatal(err)
	}
	path, err := vault.CookiePath("user-1", "tm")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != PlatformTelemost+".cookies.json" {
		t.Fatalf("CookiePath() = %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != payload {
		t.Fatalf("cookie file = %q", string(raw))
	}
}

func TestCookieVaultRejectsInvalidInputs(t *testing.T) {
	vault, err := NewCookieVault(t.TempDir(), bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.StoreSubmit("../user", tunnel.CookieSubmit{
		RequestID: "cookie-1",
		Platform:  PlatformTelemost,
		Format:    CookieFormatJSON,
		Payload:   "[]",
	}); err == nil {
		t.Fatal("StoreSubmit() expected invalid user id error")
	}
	if _, err := vault.StoreSubmit("user-1", tunnel.CookieSubmit{
		RequestID: "cookie-1",
		Platform:  PlatformTelemost,
		Format:    "netscape",
		Payload:   "[]",
	}); err == nil {
		t.Fatal("StoreSubmit() expected unsupported format error")
	}
}

func TestCookieVaultKeyFromBase64(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	got, err := CookieVaultKeyFromBase64(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("decoded key mismatch")
	}
	if _, err := CookieVaultKeyFromBase64(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("CookieVaultKeyFromBase64() expected key length error")
	}
}
