package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/emirhanusta/kymo/internal/repository"
)

// UsernamePattern is the allowed shape of a username (after lower-casing).
const UsernamePattern = `^[a-z0-9][a-z0-9._-]{1,63}$`

var usernameRe = regexp.MustCompile(UsernamePattern)

// ErrUsername describes a rejected username.
var ErrUsername = errors.New("username must be 2 to 64 characters: lower-case letters, digits, '.', '_' or '-', starting with a letter or digit")

// NormalizeUsername trims and lower-cases s and validates it.
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !usernameRe.MatchString(s) {
		return "", ErrUsername
	}
	return s, nil
}

// SanitizeUsername turns arbitrary text into a valid username: lower-cased,
// invalid runs replaced by '-', leading separators dropped, capped at 60
// characters (room for a dedupe suffix). It returns "user" when nothing usable
// remains.
func SanitizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if len(out) > 60 {
		out = out[:60]
	}
	if len(out) < 2 {
		return "user"
	}
	return out
}

// UniqueUsername returns base, or base-2, base-3, ... — the first not yet taken.
func UniqueUsername(ctx context.Context, repo repository.AuthRepository, base string) (string, error) {
	cand := base
	for n := 2; n < 10000; n++ {
		_, err := repo.GetUserByUsername(ctx, cand)
		if errors.Is(err, repository.ErrNotFound) {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
		cand = fmt.Sprintf("%s-%d", base, n)
	}
	return "", errors.New("no free username")
}
