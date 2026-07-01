package controlplane

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"whitelist-bypass/relay/tunnel"
)

const (
	CookieFormatJSON   = "json"
	CookieFormatHeader = "header"
)

var vaultNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type CookieVault struct {
	root string
	aead cipher.AEAD
	now  func() time.Time
}

type CookieRecord struct {
	UserID    string    `json:"userId"`
	Platform  string    `json:"platform"`
	Format    string    `json:"format"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

func NewCookieVault(root string, key []byte) (*CookieVault, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("controlplane: cookie vault root is required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("controlplane: cookie vault key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("controlplane: cookie vault cipher: %w", err)
	}
	return &CookieVault{root: root, aead: aead, now: time.Now}, nil
}

func CookieVaultKeyFromBase64(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("controlplane: cookie vault key must be 32 bytes, got %d", len(key))
	}
	return key, nil
}

func (v *CookieVault) SetClockForTest(now func() time.Time) {
	v.now = now
}

func (v *CookieVault) StoreSubmit(userID string, submit tunnel.CookieSubmit) (tunnel.CookieAck, error) {
	if userID == "" {
		userID = submit.UserID
	}
	if err := validateVaultName(userID); err != nil {
		return tunnel.CookieAck{}, fmt.Errorf("controlplane: invalid user id: %w", err)
	}
	platform := normalizePlatform(submit.Platform)
	if err := validateVaultName(platform); err != nil {
		return tunnel.CookieAck{}, fmt.Errorf("controlplane: invalid platform: %w", err)
	}
	format := strings.ToLower(strings.TrimSpace(submit.Format))
	if format != CookieFormatJSON && format != CookieFormatHeader {
		return tunnel.CookieAck{}, fmt.Errorf("controlplane: unsupported cookie format %q", submit.Format)
	}
	record := CookieRecord{
		UserID:    userID,
		Platform:  platform,
		Format:    format,
		Payload:   submit.Payload,
		CreatedAt: v.now().UTC(),
	}
	if err := v.store(record); err != nil {
		return tunnel.CookieAck{}, err
	}
	return tunnel.CookieAck{RequestID: submit.RequestID, Platform: platform, Stored: true}, nil
}

func (v *CookieVault) CookiePath(userID, platform string) (string, error) {
	if err := validateVaultName(userID); err != nil {
		return "", fmt.Errorf("controlplane: invalid user id: %w", err)
	}
	platform = normalizePlatform(platform)
	if err := validateVaultName(platform); err != nil {
		return "", fmt.Errorf("controlplane: invalid platform: %w", err)
	}
	record, err := v.Load(userID, platform)
	if err != nil {
		return "", err
	}
	if record.Format != CookieFormatJSON {
		return "", fmt.Errorf("controlplane: cookie format %q cannot be exposed as a JSON file", record.Format)
	}
	path := filepath.Join(v.root, userID, platform+".cookies.json")
	if err := os.WriteFile(path, []byte(record.Payload), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func (v *CookieVault) Load(userID, platform string) (CookieRecord, error) {
	if err := validateVaultName(userID); err != nil {
		return CookieRecord{}, fmt.Errorf("controlplane: invalid user id: %w", err)
	}
	platform = normalizePlatform(platform)
	if err := validateVaultName(platform); err != nil {
		return CookieRecord{}, fmt.Errorf("controlplane: invalid platform: %w", err)
	}
	raw, err := os.ReadFile(v.recordPath(userID, platform))
	if err != nil {
		return CookieRecord{}, err
	}
	if len(raw) < v.aead.NonceSize() {
		return CookieRecord{}, errors.New("controlplane: encrypted cookie record is truncated")
	}
	nonce := raw[:v.aead.NonceSize()]
	ciphertext := raw[v.aead.NonceSize():]
	plain, err := v.aead.Open(nil, nonce, ciphertext, []byte(userID+"\x00"+platform))
	if err != nil {
		return CookieRecord{}, err
	}
	var record CookieRecord
	if err := json.Unmarshal(plain, &record); err != nil {
		return CookieRecord{}, err
	}
	return record, nil
}

func (v *CookieVault) store(record CookieRecord) error {
	dir := filepath.Join(v.root, record.UserID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	plain, err := json.Marshal(record)
	if err != nil {
		return err
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := v.aead.Seal(nonce, nonce, plain, []byte(record.UserID+"\x00"+record.Platform))
	path := v.recordPath(record.UserID, record.Platform)
	tmp, err := os.CreateTemp(dir, record.Platform+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func (v *CookieVault) recordPath(userID, platform string) string {
	return filepath.Join(v.root, userID, platform+".cookies.enc")
}

func validateVaultName(name string) error {
	if !vaultNamePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}
