package snapshot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsEncryptedRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "cloud-storage")
	settings := NewSettingsFile(dir)
	if _, exists, err := settings.Load(); err != nil || exists {
		t.Fatalf("empty settings: %v %v", exists, err)
	}
	record := ConnectionRecord{Connection: Connection{AuthMode: AuthAccessKeys, Bucket: "backups", Region: "us-east-1", AccessKeyID: "example-key", SecretAccessKey: "example-secret", SessionToken: "example-token"}, UpdatedAt: time.Now()} // pragma: allowlist secret -- fake test credentials.
	if err := settings.Save(record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "connection.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"example-key", "example-secret", "example-token", "backups"} {
		if bytes.Contains(data, []byte(value)) {
			t.Fatal("unencrypted settings on disk")
		}
	}
	for _, name := range []string{"encryption.key", "connection.enc"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions %s: %v %v", name, info, err)
		}
	}
	loaded, exists, err := NewSettingsFile(dir).Load()
	if err != nil || !exists || loaded.Connection != record.Connection || !loaded.UpdatedAt.Equal(record.UpdatedAt) {
		t.Fatal("encrypted settings did not survive reopening")
	}
	if err := settings.Save(record); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(dir, "connection.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(data, again) {
		t.Fatal("encryption reused a nonce")
	}
	again[len(again)-1] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "connection.enc"), again, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.Load(); err == nil {
		t.Fatal("tampered settings accepted")
	}
}

func TestSettingsRejectsMissingKeyAndInsecurePermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settings := NewSettingsFile(dir)
	if err := settings.Save(ConnectionRecord{Disconnected: true}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "encryption.key")
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.Load(); err == nil {
		t.Fatal("public encryption key accepted")
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.Load(); err == nil {
		t.Fatal("missing key silently replaced")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("load recreated lost encryption key")
	}
}
