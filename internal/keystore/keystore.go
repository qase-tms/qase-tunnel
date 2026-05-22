package keystore

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
	"runtime"
	"sync"
)

const ServiceName = "qase.io/qase-tunnel"

var ErrNotFound = errors.New("keystore: not found")

type Keystore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

type MemoryKeystore struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemoryKeystore() *MemoryKeystore { return &MemoryKeystore{m: map[string]string{}} }

func (k *MemoryKeystore) Get(key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (k *MemoryKeystore) Set(key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = value
	return nil
}

func (k *MemoryKeystore) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

// FileKeystore persists entries to an encrypted JSON blob (AES-GCM)
// at <dir>/secrets.enc with the key alongside at <dir>/key.bin.
type FileKeystore struct {
	dir string
	mu  sync.Mutex
}

// NewFileKeystore returns a FileKeystore rooted at dir, defaulting to
// ~/.qase-tunnel when dir is empty.
func NewFileKeystore(dir string) (*FileKeystore, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".qase-tunnel")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(dir, 0o700)
	}
	return &FileKeystore{dir: dir}, nil
}

func (k *FileKeystore) Path() string { return k.dir }

func (k *FileKeystore) keyFile() string     { return filepath.Join(k.dir, "key.bin") }
func (k *FileKeystore) secretsFile() string { return filepath.Join(k.dir, "secrets.enc") }

func (k *FileKeystore) Set(key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	bag, err := k.read()
	if err != nil {
		return err
	}
	bag[key] = value
	return k.write(bag)
}

func (k *FileKeystore) Get(key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	bag, err := k.read()
	if err != nil {
		return "", err
	}
	v, ok := bag[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (k *FileKeystore) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	bag, err := k.read()
	if err != nil {
		return err
	}
	if _, ok := bag[key]; !ok {
		return nil
	}
	delete(bag, key)
	return k.write(bag)
}

func (k *FileKeystore) read() (map[string]string, error) {
	keyBytes, err := k.loadOrCreateKey()
	if err != nil {
		return nil, err
	}

	encoded, err := os.ReadFile(k.secretsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(encoded) == 0 {
		return map[string]string{}, nil
	}

	plaintext, err := decrypt(keyBytes, encoded)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets: %w", err)
	}

	bag := map[string]string{}
	if err := json.Unmarshal(plaintext, &bag); err != nil {
		return nil, fmt.Errorf("decode secrets: %w", err)
	}
	return bag, nil
}

func (k *FileKeystore) write(bag map[string]string) error {
	keyBytes, err := k.loadOrCreateKey()
	if err != nil {
		return err
	}

	plaintext, err := json.Marshal(bag)
	if err != nil {
		return err
	}

	encoded, err := encrypt(keyBytes, plaintext)
	if err != nil {
		return err
	}

	return writeAtomic(k.secretsFile(), encoded, 0o600)
}

func (k *FileKeystore) loadOrCreateKey() ([]byte, error) {
	if data, err := os.ReadFile(k.keyFile()); err == nil {
		decoded, decErr := base64.StdEncoding.DecodeString(string(data))
		if decErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := writeAtomic(k.keyFile(), []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, sealed...), nil
}

func decrypt(key, payload []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) < gcm.NonceSize() {
		return nil, errors.New("payload too short")
	}
	nonce, sealed := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	return gcm.Open(nil, nonce, sealed, nil)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".keystore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
