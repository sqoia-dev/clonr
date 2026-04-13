package server

import (
	"testing"
	"time"
)

func testSecret() []byte { return []byte("test-secret-key-for-unit-tests-32b") }

func TestSignAndValidate_HappyPath(t *testing.T) {
	secret := testSecret()
	p := newSessionPayload("abcd1234")

	token, err := signSessionToken(secret, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	result, err := validateSessionToken(secret, token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.payload.Scope != "admin" {
		t.Errorf("scope: got %q, want admin", result.payload.Scope)
	}
	if result.payload.Kid != "abcd1234" {
		t.Errorf("kid: got %q, want abcd1234", result.payload.Kid)
	}
	if result.needsReissue {
		t.Error("fresh token should not need reissue")
	}
}

func TestValidate_TamperedSignature(t *testing.T) {
	secret := testSecret()
	p := newSessionPayload("abcd1234")

	token, err := signSessionToken(secret, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper the last byte of the signature.
	tampered := token[:len(token)-1] + "X"
	_, err = validateSessionToken(secret, tampered)
	if err == nil {
		t.Fatal("expected error on tampered token, got nil")
	}
}

func TestValidate_ExpiredToken(t *testing.T) {
	secret := testSecret()
	p := newSessionPayload("abcd1234")
	// Back-date the expiry.
	p.EXP = time.Now().Add(-1 * time.Hour).Unix()

	token, err := signSessionToken(secret, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = validateSessionToken(secret, token)
	if err == nil {
		t.Fatal("expected error on expired token, got nil")
	}
}

func TestValidate_SlidingReissue(t *testing.T) {
	secret := testSecret()
	p := newSessionPayload("abcd1234")
	// Set slide to >30m ago so it triggers a reissue.
	p.Slide = time.Now().Add(-45 * time.Minute).Unix()

	token, err := signSessionToken(secret, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	result, err := validateSessionToken(secret, token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.needsReissue {
		t.Error("expected needsReissue=true for session idle >30m")
	}
}

func TestSlideSessionPayload(t *testing.T) {
	p := newSessionPayload("abcd1234")
	// Back-date Slide and EXP by 1 hour so slideSessionPayload must advance them.
	p.Slide = time.Now().Add(-1 * time.Hour).Unix()
	p.EXP = time.Now().Add(-1 * time.Hour).Unix()
	oldSlide := p.Slide
	oldEXP := p.EXP

	slid := slideSessionPayload(p)

	if slid.Slide <= oldSlide {
		t.Error("slide timestamp should have advanced")
	}
	if slid.EXP <= oldEXP {
		t.Error("expiry should have advanced after slide")
	}
	if slid.Scope != p.Scope {
		t.Error("scope should be unchanged after slide")
	}
}

func TestValidate_WrongSecret(t *testing.T) {
	p := newSessionPayload("abcd1234")
	token, err := signSessionToken([]byte("secret-A"), p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = validateSessionToken([]byte("secret-B"), token)
	if err == nil {
		t.Fatal("expected error when validating with wrong secret")
	}
}

func TestValidate_MalformedToken(t *testing.T) {
	secret := testSecret()
	for _, bad := range []string{"", "nodot", "a.b.c", "...", "!!!.!!!"} {
		_, err := validateSessionToken(secret, bad)
		if err == nil {
			t.Errorf("expected error for malformed token %q", bad)
		}
	}
}
