// Package secret encrypts small secrets (e.g. uploaded kubeconfigs) at rest
// with AES-256-GCM. The key is derived from the KYMO_SECRET_KEY setting.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// ErrNoKey is returned when encryption is requested but no key is configured.
var ErrNoKey = errors.New("secret key not configured: set KYMO_SECRET_KEY to store uploaded kubeconfigs")

// Box seals and opens secrets. The zero value (or a Box built from an empty
// passphrase) is unconfigured and refuses to encrypt or decrypt.
type Box struct {
	aead cipher.AEAD
}

// New derives a 256-bit key from passphrase via SHA-256. An empty passphrase
// yields an unconfigured Box.
func New(passphrase string) (*Box, error) {
	if passphrase == "" {
		return &Box{}, nil
	}
	key := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Configured reports whether a key is available.
func (b *Box) Configured() bool { return b != nil && b.aead != nil }

// Seal encrypts plaintext; the random nonce is prepended to the ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts data produced by Seal.
func (b *Box) Open(data []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrNoKey
	}
	n := b.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("secret: ciphertext too short")
	}
	out, err := b.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return nil, errors.New("secret: cannot decrypt (wrong KYMO_SECRET_KEY?)")
	}
	return out, nil
}
