// secrets_test.go — unit tests for AES-256-GCM encryption helpers (S1-15/16).
package secrets_test

import (
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/secrets"
)

// ─── ValidateKey ─────────────────────────────────────────────────────────────

func TestValidateKey_Missing(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "")
	if err := secrets.ValidateKey(); err == nil {
		t.Error("expected error for empty key, got nil")
	}
}

func TestValidateKey_Default(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "clustr-default-secret-v1")
	if err := secrets.ValidateKey(); err == nil {
		t.Error("expected error for default key, got nil")
	}
}

func TestValidateKey_Valid(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if err := secrets.ValidateKey(); err != nil {
		t.Errorf("expected no error for valid key, got: %v", err)
	}
}

// ─── EncryptWithKey / DecryptWithKey ────────────────────────────────────────

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	plaintext := []byte("ldap-bind-password-secret!")
	ciphertext, err := secrets.EncryptWithKey(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptWithKey: %v", err)
	}

	if len(ciphertext) < 32 {
		t.Errorf("ciphertext too short (%d chars), expected >= 32 hex chars", len(ciphertext))
	}

	// Ciphertext must be valid hex.
	if strings.ContainsRune(ciphertext, ' ') {
		t.Error("ciphertext contains spaces — not valid hex")
	}

	recovered, err := secrets.DecryptWithKey(key, ciphertext)
	if err != nil {
		t.Fatalf("DecryptWithKey: %v", err)
	}
	if string(recovered) != string(plaintext) {
		t.Errorf("recovered = %q, want %q", recovered, plaintext)
	}
}

func TestEncryptWithKey_Randomness(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}

	c1, err := secrets.EncryptWithKey(key, []byte("same plaintext"))
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	c2, err := secrets.EncryptWithKey(key, []byte("same plaintext"))
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}
	if c1 == c2 {
		t.Error("two encryptions of the same plaintext produced identical ciphertext — nonce is not random")
	}
}

func TestDecryptWithKey_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x99
	}

	ciphertext, err := secrets.EncryptWithKey(key, []byte("secret data"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Flip a byte at the end of the hex string to simulate tampering.
	tampered := ciphertext[:len(ciphertext)-2] + "00"

	_, err = secrets.DecryptWithKey(key, tampered)
	if err == nil {
		t.Error("expected error for tampered ciphertext, got nil")
	}
}

func TestDecryptWithKey_WrongKey(t *testing.T) {
	rightKey := make([]byte, 32)
	for i := range rightKey {
		rightKey[i] = 0x11
	}
	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = 0x22
	}

	ciphertext, err := secrets.EncryptWithKey(rightKey, []byte("private data"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = secrets.DecryptWithKey(wrongKey, ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with wrong key, got nil")
	}
}

func TestDecryptWithKey_InvalidHex(t *testing.T) {
	key := make([]byte, 32)
	_, err := secrets.DecryptWithKey(key, "not-valid-hex!!!")
	if err == nil {
		t.Error("expected error for invalid hex input, got nil")
	}
}

func TestDecryptWithKey_TooShort(t *testing.T) {
	key := make([]byte, 32)
	// 10 hex chars = 5 bytes — too short for a GCM nonce (12 bytes).
	_, err := secrets.DecryptWithKey(key, "aabbccddee")
	if err == nil {
		t.Error("expected error for too-short ciphertext, got nil")
	}
}

// ─── Encrypt / Decrypt via env key ───────────────────────────────────────────

func TestEncryptDecrypt_ViaEnvKey(t *testing.T) {
	// Use a valid 32-byte hex key.
	t.Setenv("CLUSTR_SECRET_KEY", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")

	plaintext := "service-bind-password-value"
	ciphertext, err := secrets.Encrypt([]byte(plaintext))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	recovered, err := secrets.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(recovered) != plaintext {
		t.Errorf("Decrypt = %q, want %q", recovered, plaintext)
	}
}

func TestEncrypt_ReturnsErrorIfKeyUnset(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "")
	_, err := secrets.Encrypt([]byte("data"))
	if err == nil {
		t.Error("expected error for unset key, got nil")
	}
}

// ─── DeriveKey ───────────────────────────────────────────────────────────────

func TestDeriveKey_HexKey_ReturnsRawBytes(t *testing.T) {
	// A 32-byte hex key (64 hex chars) should be decoded as raw bytes.
	hexKey := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	t.Setenv("CLUSTR_SECRET_KEY", hexKey)

	key, err := secrets.DeriveKey()
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("DeriveKey returned %d bytes, want 32", len(key))
	}
}

func TestDeriveKey_ArbitraryString_Hashes(t *testing.T) {
	// A non-hex string should hash to 32 bytes via SHA-256 fallback.
	t.Setenv("CLUSTR_SECRET_KEY", "this-is-not-hex-but-valid-for-hashing")

	key, err := secrets.DeriveKey()
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("DeriveKey returned %d bytes, want 32", len(key))
	}
}

func TestDeriveKey_Deterministic(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "my-static-passphrase")
	k1, err := secrets.DeriveKey()
	if err != nil {
		t.Fatalf("first DeriveKey: %v", err)
	}
	k2, err := secrets.DeriveKey()
	if err != nil {
		t.Fatalf("second DeriveKey: %v", err)
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Error("DeriveKey is not deterministic for the same key string")
			break
		}
	}
}
