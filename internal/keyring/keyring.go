// Package keyring seals and opens secrets with AES-256-GCM under a set of
// master keys (docs/design/auth-keys.md §4.4).
package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	keySize   = 32
	nonceSize = 12
)

// Keyring holds master keys by id; the first parsed is the primary, used for
// every Seal.
type Keyring struct {
	primary string
	aeads   map[string]cipher.AEAD
}

// Parse reads a comma list of id:base64(32 bytes), primary first.
func Parse(env string) (*Keyring, error) {
	if strings.TrimSpace(env) == "" {
		return nil, errors.New("no keys")
	}
	kr := &Keyring{aeads: map[string]cipher.AEAD{}}
	for entry := range strings.SplitSeq(env, ",") {
		id, b64, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || id == "" {
			return nil, errors.New("entry is not id:base64")
		}
		if _, dup := kr.aeads[id]; dup {
			return nil, fmt.Errorf("duplicate key id %q", id)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("key %q: bad base64", id)
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("key %q: want %d bytes, got %d", id, keySize, len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", id, err)
		}
		if kr.primary == "" {
			kr.primary = id
		}
		kr.aeads[id] = aead
	}
	return kr, nil
}

// Primary returns the id of the key Seal uses.
func (k *Keyring) Primary() string { return k.primary }

// Seal encrypts plaintext under the primary key, binding aad. ct is a random
// nonce followed by the GCM output.
func (k *Keyring) Seal(plaintext, aad []byte) (ct []byte, keyID string, err error) {
	aead := k.aeads[k.primary]
	nonce := make([]byte, nonceSize, nonceSize+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("read nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), k.primary, nil
}

// Open decrypts ct sealed under keyID. It fails on an unknown keyID, a
// different aad, or a modified ct.
func (k *Keyring) Open(ct []byte, keyID string, aad []byte) ([]byte, error) {
	aead, ok := k.aeads[keyID]
	if !ok {
		return nil, fmt.Errorf("unknown key id %q", keyID)
	}
	if len(ct) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	pt, err := aead.Open(nil, ct[:nonceSize], ct[nonceSize:], aad)
	if err != nil {
		return nil, errors.New("authentication failed")
	}
	return pt, nil
}
