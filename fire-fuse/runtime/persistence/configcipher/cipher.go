// Package configcipher provides authenticated encryption for configuration
// values stored by Runtime persistence adapters.
package configcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

const KeyBytes = 32

var (
	ErrInvalidKey     = errors.New("invalid configuration encryption key")
	ErrUnavailable    = errors.New("configuration encryption is unavailable")
	ErrAuthentication = errors.New("configuration ciphertext authentication failed")
)

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("%w: key must contain exactly %d bytes", ErrInvalidKey, KeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("construct configuration cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("construct configuration AEAD: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(scope, scopeID, name, value string) ([]byte, []byte, error) {
	if c == nil {
		return nil, nil, ErrUnavailable
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate configuration nonce: %w", err)
	}
	ciphertext := c.aead.Seal(nil, nonce, []byte(value), additionalData(scope, scopeID, name))
	return nonce, ciphertext, nil
}

func (c *Cipher) Open(scope, scopeID, name string, nonce, ciphertext []byte) (string, error) {
	if c == nil {
		return "", ErrUnavailable
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, additionalData(scope, scopeID, name))
	if err != nil {
		return "", errors.Join(ErrAuthentication, err)
	}
	return string(plaintext), nil
}

func additionalData(scope, scopeID, name string) []byte {
	return []byte("cantelop-config-v1\x00" + scope + "\x00" + scopeID + "\x00" + name)
}
