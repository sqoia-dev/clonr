// Package secrets provides AES-256-GCM encryption helpers shared across
// clustr modules (LDAP, BMC, Slurm). All credentials stored in the SQLite
// database are encrypted with a key derived from CLUSTR_SECRET_KEY.
//
// Security model:
//   - CLUSTR_SECRET_KEY must be a 32-byte hex string (openssl rand -hex 32).
//   - The server hard-fails on startup if the key is unset or is the insecure
//     default, preventing silent deployment with a publicly-known key.
//   - Ciphertext is stored as hex(nonce || GCM-sealed(plaintext)).
//   - Each encrypt call generates a fresh random nonce.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// defaultSecretKey is a canary value. Any deployment using this key is insecure
// because it is committed to the public repository. The server hard-fails if
// CLUSTR_SECRET_KEY equals this string or is empty.
const defaultSecretKey = "clustr-default-secret-v1"

// ErrKeyRequired is returned when CLUSTR_SECRET_KEY is unset or is the default.
var ErrKeyRequired = fmt.Errorf(
	"CLUSTR_SECRET_KEY is not set or is the insecure default. " +
		"Set CLUSTR_SECRET_KEY to a strong random value (openssl rand -hex 32) " +
		"and restart clustr-serverd",
)

// ValidateKey returns ErrKeyRequired if CLUSTR_SECRET_KEY is unset or the default.
// Call this at module enable-time to prevent activation without a real key.
func ValidateKey() error {
	k := os.Getenv("CLUSTR_SECRET_KEY")
	if k == "" || k == defaultSecretKey {
		return ErrKeyRequired
	}
	return nil
}

// DeriveKey derives a 32-byte AES key from CLUSTR_SECRET_KEY.
// Returns ErrKeyRequired if the key is unset or is the insecure default.
func DeriveKey() ([]byte, error) {
	envKey := os.Getenv("CLUSTR_SECRET_KEY")
	if envKey == "" || envKey == defaultSecretKey {
		return nil, ErrKeyRequired
	}
	// Prefer a raw 32-byte hex key (standard format: openssl rand -hex 32).
	raw, err := hex.DecodeString(envKey)
	if err == nil && len(raw) == 32 {
		return raw, nil
	}
	// Fallback: hash arbitrary string to 32 bytes (allows non-hex keys).
	h := sha256.Sum256([]byte(envKey))
	return h[:], nil
}

// Encrypt encrypts plaintext with AES-256-GCM using the key from CLUSTR_SECRET_KEY.
// Returns hex(nonce || ciphertext || tag).
func Encrypt(plaintext []byte) (string, error) {
	key, err := DeriveKey()
	if err != nil {
		return "", err
	}
	return EncryptWithKey(key, plaintext)
}

// Decrypt decrypts a hex-encoded AES-256-GCM ciphertext using CLUSTR_SECRET_KEY.
func Decrypt(ciphertextHex string) ([]byte, error) {
	key, err := DeriveKey()
	if err != nil {
		return nil, err
	}
	return DecryptWithKey(key, ciphertextHex)
}

// EncryptWithKey encrypts plaintext with AES-256-GCM using the provided key.
// Returns hex(nonce || ciphertext || tag).
func EncryptWithKey(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("secrets: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secrets: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(ciphertext), nil
}

// DecryptWithKey decrypts a hex-encoded AES-256-GCM ciphertext with the provided key.
func DecryptWithKey(key []byte, ciphertextHex string) ([]byte, error) {
	data, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return nil, fmt.Errorf("secrets: decode hex: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	nonce, ct := data[:ns], data[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}
