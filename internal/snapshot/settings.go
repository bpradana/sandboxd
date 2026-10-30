package snapshot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const settingsMagic = "sandboxd-cloud-settings-v1\x00"

// SettingsFile encrypts credentials with a per-daemon AES-256-GCM key. The
// encryption key and ciphertext must remain on the daemon's persistent disk.
// One daemon owns the directory; methods serialize writes within that daemon.
type SettingsFile struct {
	dir string
	mu  sync.Mutex
}

func NewSettingsFile(dir string) *SettingsFile { return &SettingsFile{dir: dir} }

func (s *SettingsFile) Load() (ConnectionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var record ConnectionRecord
	data, err := readPrivateFile(filepath.Join(s.dir, "connection.enc"), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	aead, err := s.cipher(false)
	if err != nil {
		return record, false, err
	}
	if !bytes.HasPrefix(data, []byte(settingsMagic)) {
		return record, false, fmt.Errorf("invalid encrypted cloud storage settings")
	}
	payload := data[len(settingsMagic):]
	if len(payload) < aead.NonceSize()+aead.Overhead() {
		return record, false, fmt.Errorf("invalid encrypted cloud storage settings")
	}
	plain, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(settingsMagic))
	if err != nil {
		return record, false, fmt.Errorf("cannot decrypt cloud storage settings")
	}
	if err := json.Unmarshal(plain, &record); err != nil {
		return record, false, fmt.Errorf("invalid cloud storage settings")
	}
	return record, true, nil
}

func (s *SettingsFile) Save(record ConnectionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !filepath.IsAbs(s.dir) {
		return fmt.Errorf("cloud storage settings directory must be absolute")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("cloud storage settings directory must not be a symlink")
	}
	if err := os.Chmod(s.dir, 0o700); err != nil { // #nosec G302 -- private directory needs owner traversal; key and settings files use 0600.
		return err
	}
	aead, err := s.cipher(true)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(record)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := append([]byte(settingsMagic), nonce...)
	data = aead.Seal(data, nonce, plain, []byte(settingsMagic))
	return s.writeAtomic("connection.enc", data)
}

func (s *SettingsFile) cipher(create bool) (cipher.AEAD, error) {
	key, err := readPrivateFile(filepath.Join(s.dir, "encryption.key"), 32)
	if errors.Is(err, os.ErrNotExist) && create {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = s.writeAtomic("encryption.key", key); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read cloud storage encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid cloud storage encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func readPrivateFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("cloud storage settings and keys must be regular files readable only by their owner")
	}
	f, err := os.Open(name) // #nosec G304 -- fixed filename inside the daemon-only settings directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("cloud storage settings file exceeds its size limit")
	}
	return data, nil
}

func (s *SettingsFile) writeAtomic(name string, data []byte) error {
	f, err := os.CreateTemp(s.dir, ".settings-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(s.dir, name)); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
