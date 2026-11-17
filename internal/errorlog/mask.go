// Package errorlog holds the pure logic of error-log capture: detecting error
// lines, grouping stack traces, normalising messages into signature templates
// and masking secrets. It has no I/O.
package errorlog

import (
	"regexp"
	"strings"
)

// Mask replaces secrets in s with "***". It is best-effort: logs can contain
// sensitive data in forms this does not recognise.
func Mask(s string) string {
	if s == "" {
		return s
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}***@")
	s = bearerRe.ReplaceAllString(s, "${1} ***")
	s = jwtRe.ReplaceAllString(s, "***")
	s = awsKeyRe.ReplaceAllString(s, "***")
	s = kvSecretRe.ReplaceAllStringFunc(s, maskKV)
	s = cardSepRe.ReplaceAllString(s, "***")
	s = cardRe.ReplaceAllString(s, "***")
	return s
}

var (
	urlUserinfo = regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)
	bearerRe    = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{6,}`)
	jwtRe       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
	awsKeyRe    = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	// key (optionally quoted) [:=] value (quoted or bare)
	kvSecretRe = regexp.MustCompile(`(?i)(["']?[\w.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|private[_-]?key)[\w.-]*["']?\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&}\])"']+)`)
	cardSepRe  = regexp.MustCompile(`\b\d{4}(?:[ -]\d{4}){2,3}(?:[ -]\d{1,3})?\b`)
	cardRe     = regexp.MustCompile(`\b\d{13,19}\b`)
)

func maskKV(m string) string {
	sub := kvSecretRe.FindStringSubmatch(m)
	if sub == nil {
		return m
	}
	key, val := sub[1], sub[2]
	if val == "***" || val == `"***"` {
		return m
	}
	switch {
	case strings.HasPrefix(val, `"`):
		return key + `"***"`
	case strings.HasPrefix(val, `'`):
		return key + `'***'`
	}
	return key + "***"
}
