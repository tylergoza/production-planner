// Package secrets encrypts settings that hold passwords and tokens, so a
// downloaded or copied database file doesn't carry them in plain text.
//
// The key is 32 random bytes in a file next to the database (secrets.key,
// mode 0600), made on first start. Sealed values look like
// "enc:v1:<base64 nonce+ciphertext>" (AES-256-GCM). Someone with the whole
// server has the key too; this protects the database file on its own.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

const prefix = "enc:v1:"

// ErrUnreadable means a sealed value can't be opened with this key: most
// likely the database was restored onto a server without its secrets.key.
var ErrUnreadable = errors.New("can't read the saved secret (secrets.key is missing or different): enter it again")

type Key struct {
	aead cipher.AEAD
}

// LoadOrCreate reads the key file at path, making it if it doesn't exist.
func LoadOrCreate(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		// O_EXCL: if another process made it first, use theirs.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return LoadOrCreate(path)
		}
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s should hold 32 bytes, not %d", path, len(b))
	}
	return New(b)
}

// New makes a key from 32 bytes.
func New(b []byte) (*Key, error) {
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

// IsSealed reports whether v is a sealed value rather than plain text.
func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }

// Seal encrypts v. Blank stays blank, so "not set" needs no key.
func (k *Key) Seal(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := k.aead.Seal(nonce, nonce, []byte(v), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value. A plain value (saved before secrets were
// encrypted) comes back as it is.
func (k *Key) Open(v string) (string, error) {
	if !IsSealed(v) {
		return v, nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, prefix))
	n := k.aead.NonceSize()
	if err != nil || len(b) < n {
		return "", ErrUnreadable
	}
	plain, err := k.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return "", ErrUnreadable
	}
	return string(plain), nil
}
