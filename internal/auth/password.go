// Package auth implements KYMO's accounts and access control: password
// hashing, sessions, API tokens, roles, CSRF/CORS rules, OIDC single sign-on
// and first-run bootstrap.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP-recommended class: 64 MiB, 3 passes, 2 lanes).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonKeyLen    = 32
	argonSaltLen   = 16

	// MinPasswordLength / MaxPasswordLength bound accepted passwords. The upper
	// bound stops absurdly long inputs from being used to burn CPU.
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// ErrPasswordPolicy describes a rejected password.
var ErrPasswordPolicy = fmt.Errorf("password must be %d to %d characters long", MinPasswordLength, MaxPasswordLength)

// ValidatePassword enforces the password policy.
func ValidatePassword(pw string) error {
	n := len([]rune(pw))
	if n < MinPasswordLength || len(pw) > MaxPasswordLength {
		return ErrPasswordPolicy
	}
	return nil
}

// HashPassword returns an encoded argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash> (base64, unpadded).
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks pw against an encoded hash in constant time. A
// malformed hash never verifies.
func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	// Refuse parameters that would let a corrupted/hostile row exhaust memory.
	if mem == 0 || mem > 1024*1024 || t == 0 || t > 20 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash string

func init() { dummyHash, _ = HashPassword("kymo-dummy-password") }

// BurnPasswordCheck spends the time of a real verification; used for unknown
// users so response time does not reveal whether an email exists.
func BurnPasswordCheck(pw string) { VerifyPassword(pw, dummyHash) }

var errEmail = errors.New("invalid email address")

// NormalizeEmail trims and lower-cases an email and checks its basic shape.
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 || len(s) > 254 || strings.ContainsAny(s, " \t\r\n<>,;\"") || !strings.Contains(s[at+1:], ".") {
		return "", errEmail
	}
	return s, nil
}
