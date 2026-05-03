// crypt.go — password hashing for LDAP userPassword using {CRYPT} with glibc $6$
// SHA-512-crypt at 100k rounds.
//
// slapd is configured with:
//   olcPasswordHash: {CRYPT}
//   olcPasswordCryptSaltFormat: $6$rounds=100000$%.16s
//
// We pre-hash on the Go side and store the full {CRYPT}$6$... string directly in
// userPassword. This is equivalent to slapd hashing a plaintext password — both
// produce a valid {CRYPT}$6$... value — but pre-hashing on the Go side means we
// never transmit a plaintext password over the LDAP connection for password sets.
//
// For slapadd seed LDIF (DM + service account passwords), we pass plaintext and let
// slapd hash them at load time via olcPasswordHash. That is safe because slapadd
// runs locally as root over a Unix socket, not over the network.
//
// No CGO. github.com/GehirnInc/crypt/sha512_crypt is pure Go.

package ldap

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// cryptBase64Alphabet is the alphabet used by crypt(3) — NOT standard base64.
// It is: ./0-9A-Za-z
// The GehirnInc sha512_crypt package uses this internally; we need it only to
// encode the random salt bytes.
const cryptBase64Alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// cryptBase64Encode encodes src bytes using the crypt(3) base64 alphabet.
// Output length = ceil(len(src) * 4 / 3), not padded.
func cryptBase64Encode(src []byte) string {
	// Use stdlib base64 with a custom alphabet (no padding).
	enc := base64.NewEncoding(cryptBase64Alphabet).WithPadding(base64.NoPadding)
	return enc.EncodeToString(src)
}

// HashPasswordCrypt generates a {CRYPT}$6$rounds=100000$<salt>$<hash> string
// suitable for direct storage in the LDAP userPassword attribute.
//
// slapd stores passwords in the {SCHEME}value format; prepending {CRYPT} tells
// slapd to verify future bind attempts using crypt(3).
func HashPasswordCrypt(password string) (string, error) {
	// Generate 16 bytes of random salt.
	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", fmt.Errorf("ldap crypt: generate salt: %w", err)
	}

	// Encode the salt using the crypt base64 alphabet and take at most 16 chars.
	// crypt(3) accepts up to 16 salt characters for $6$.
	saltStr := cryptBase64Encode(saltBytes)
	if len(saltStr) > 16 {
		saltStr = saltStr[:16]
	}

	// Build the full salt prefix that crypt expects:  $6$rounds=100000$<salt>$
	saltPrefix := fmt.Sprintf("$6$rounds=100000$%s$", saltStr)

	// Hash the password.
	crypt := sha512_crypt.New()
	hashed, err := crypt.Generate([]byte(password), []byte(saltPrefix))
	if err != nil {
		return "", fmt.Errorf("ldap crypt: sha512_crypt.Generate: %w", err)
	}

	// hashed is already in the form: $6$rounds=100000$<salt>$<hash>
	// Prepend {CRYPT} for the LDAP userPassword format.
	return "{CRYPT}" + hashed, nil
}
