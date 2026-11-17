package errorlog

import (
	"crypto/sha1" //nolint:gosec // identifier, not security
	"encoding/hex"
	"regexp"
	"strings"
)

// MaxTemplateLen caps a signature template (in runes).
const MaxTemplateLen = 300

// SignatureID returns the stable id of a signature: the first 16 hex chars of
// sha1(workloadID + "\x00" + template).
func SignatureID(workloadID, template string) string {
	sum := sha1.Sum([]byte(workloadID + "\x00" + template)) //nolint:gosec
	return hex.EncodeToString(sum[:])[:16]
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

	tsPatterns = `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?` +
		`|\d{4}/\d{2}/\d{2}[ -]\d{2}:\d{2}:\d{2}(?:\.\d+)?` +
		`|\d{2}/[A-Z][a-z]{2}/\d{4}:\d{2}:\d{2}:\d{2}(?: [+-]\d{4})?` +
		`|[A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}(?:\.\d+)?` +
		`|\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`
	tsRe      = regexp.MustCompile(tsPatterns)
	leadTsRe  = regexp.MustCompile(`^\[?(?:` + tsPatterns + `)\]?[ ,:-]*`)
	klogHdrRe = regexp.MustCompile(`^[IWEF]\d{4} \d{2}:\d{2}:\d{2}(?:\.\d+)?\s+\d+ \S+?\] ?`)
	// level prefix: ERROR, [error], <WARN>, level=error ...
	leadLvlRe = regexp.MustCompile(`^(?:\[|<|\()?(?i:trace|debug|info|notice|warn|warning|error|err|fatal|panic|critical|crit|severe|emerg|alert)(?:\]|>|\))?(?::|\s+-|\s)\s*`)
	// "[main] " / "[http-nio-8080-exec-1] " thread brackets and "logger.name - "
	bracketRe = regexp.MustCompile(`^\[[^\]\n]{1,60}\]\s*`)
	loggerRe  = regexp.MustCompile(`^[\w.$]+\s+-\s+`)

	uuidRe  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	emailRe = regexp.MustCompile(`\b[\w.+-]+@[\w-]+(?:\.[\w-]+)+\b`)
	ipRe    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{1,5})?\b`)
	hexRe   = regexp.MustCompile(`\b(?:0x[0-9a-fA-F]+|[0-9a-fA-F]{8,})\b`)
	numRe   = regexp.MustCompile(`\b\d+(?:\.\d+)*\b`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// StripANSI removes terminal colour escape sequences.
func StripANSI(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// stripPrefix removes a leading timestamp, klog header, level and logger
// decorations from a plain-text log line.
func stripPrefix(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < 6; i++ {
		before := s
		if loc := klogHdrRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		}
		if loc := leadTsRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		}
		if loc := leadLvlRe.FindStringIndex(s); loc != nil {
			rest := s[loc[1]:]
			// "Error: boom" keeps its error class; only strip real level tokens.
			if !startsErrorClass(s) {
				s = rest
			}
		}
		s = strings.TrimSpace(s)
		if loc := bracketRe.FindStringIndex(s); loc != nil && i > 0 {
			s = s[loc[1]:]
		}
		if loc := loggerRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		}
		if s == before {
			break
		}
	}
	return strings.TrimSpace(s)
}

// startsErrorClass reports whether s begins like "Error: x" / "TypeError: x",
// where the leading word is part of the message rather than a level.
func startsErrorClass(s string) bool {
	return len(s) > 5 && (strings.HasPrefix(s, "Error:") || strings.HasPrefix(s, "Error ") || strings.HasPrefix(s, "panic:"))
}

// Normalize turns a (masked) error message into its signature template:
// volatile values become placeholders, whitespace collapses, length is capped.
func Normalize(msg string) string {
	s := StripANSI(msg)
	s = stripPrefix(s)
	s = tsRe.ReplaceAllString(s, "<ts>")
	s = uuidRe.ReplaceAllString(s, "<uuid>")
	s = emailRe.ReplaceAllString(s, "<email>")
	s = ipRe.ReplaceAllString(s, "<ip>")
	s = replaceQuoted(s)
	s = hexRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "0x") || strings.ContainsAny(m, "0123456789") && hasLetter(m) {
			return "<hex>"
		}
		return m
	})
	s = numRe.ReplaceAllString(s, "<n>")
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > MaxTemplateLen {
		s = string(r[:MaxTemplateLen])
	}
	return s
}

func hasLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return true
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// replaceQuoted replaces '...' and "..." spans with <str>. An opening quote
// must not follow a word character (so "can't" is not an opening quote) and a
// closing quote must not be followed by one.
func replaceQuoted(s string) string {
	if !strings.ContainsAny(s, `'"`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if (c == '\'' || c == '"') && (i == 0 || !isWordByte(s[i-1])) {
			end := -1
			for j := i + 1; j < len(s); j++ {
				if s[j] == '\\' {
					j++
					continue
				}
				if s[j] == c && (j+1 == len(s) || !isWordByte(s[j+1])) {
					end = j
					break
				}
			}
			if end > 0 {
				b.WriteString("<str>")
				i = end + 1
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
