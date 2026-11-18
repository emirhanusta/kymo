package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// RandomToken returns n random bytes, base64url-encoded without padding.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex sha256 of a high-entropy secret (cookie value, API
// token, link token). Only this hash is stored.
func HashToken(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// API token format: kymo_<prefix>_<secret>; the prefix is 8 hex chars shown in
// the UI so tokens can be told apart.
const (
	apiTokenLead      = "kymo_"
	apiTokenPrefixLen = 8
)

// NewAPIToken generates a token; token is shown once, prefix is stored.
func NewAPIToken() (token, prefix string, err error) {
	p := make([]byte, apiTokenPrefixLen/2)
	if _, err = rand.Read(p); err != nil {
		return
	}
	secret, err := RandomToken(32)
	if err != nil {
		return
	}
	prefix = hex.EncodeToString(p)
	return apiTokenLead + prefix + "_" + secret, prefix, nil
}

// LooksLikeAPIToken reports whether s has the API token shape.
func LooksLikeAPIToken(s string) bool {
	return len(s) > len(apiTokenLead)+apiTokenPrefixLen+1+20 &&
		strings.HasPrefix(s, apiTokenLead) && s[len(apiTokenLead)+apiTokenPrefixLen] == '_'
}
