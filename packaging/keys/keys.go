// Package keys is a lightweight helper for Ed25519 key management — generate,
// save, and load. It is optional: packaging works with plain ed25519.PublicKey
// and ed25519.PrivateKey directly. Use this package for the common path; skip it
// when you bring your own key management (KMS, HSM, env vars).
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
)

// New generates a new Ed25519 key pair.
func New() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// Save writes a key to a PEM file, auto-detecting the key type:
//
//	ed25519.PrivateKey → "ED25519 PRIVATE KEY" (mode 0600)
//	ed25519.PublicKey  → "ED25519 PUBLIC KEY"  (mode 0644)
func Save(path string, key any) error {
	switch k := key.(type) {
	case ed25519.PrivateKey:
		block := &pem.Block{
			Type:  "ED25519 PRIVATE KEY",
			Bytes: []byte(k),
		}
		return os.WriteFile(path, pem.EncodeToMemory(block), 0600)
	case ed25519.PublicKey:
		block := &pem.Block{
			Type:  "ED25519 PUBLIC KEY",
			Bytes: []byte(k),
		}
		return os.WriteFile(path, pem.EncodeToMemory(block), 0644)
	default:
		return fmt.Errorf("unsupported key type: %T", key)
	}
}

// Private reads an Ed25519 private key from a PEM file.
func Private(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	if block.Type != "ED25519 PRIVATE KEY" {
		return nil, fmt.Errorf("unexpected key type: %s", block.Type)
	}
	return ed25519.PrivateKey(block.Bytes), nil
}

// Public reads an Ed25519 public key from a PEM file.
func Public(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	if block.Type != "ED25519 PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected key type: %s", block.Type)
	}
	return ed25519.PublicKey(block.Bytes), nil
}
