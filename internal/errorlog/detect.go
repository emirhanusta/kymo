package errorlog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Limits of a captured entry.
const (
	// MaxLineLen is the longest single log line kept (bytes).
	MaxLineLen = 8 * 1024
	// MaxMessageLen caps the stored multi-line message (bytes).
	MaxMessageLen = 8 * 1024
	// MaxSampleLen caps the stored one-line sample (runes).
	MaxSampleLen = 500
)

// Settings is the live, hot-reloadable capture configuration.
type Settings struct {
	Enabled             bool
	MaxStreams          int
	MaxLinesPerMinute   int
	SamplesPerSignature int
	Analyzer            *Analyzer
}

// Entry is one analysed error entry (a log line plus its stack trace).
type Entry struct {
	Level    string // "error" | "fatal"
	Template string // normalised first line
	Sample   string // masked first line
	Message  string // masked full multi-line text
}

// Analyzer decides which log lines are errors. It is immutable and safe for
// concurrent use.
type Analyzer struct {
	extra  []*regexp.Regexp
	ignore []*regexp.Regexp
}

// NewAnalyzer compiles the user supplied extra and ignore patterns.
func NewAnalyzer(extra, ignore []string) (*Analyzer, error) {
	a := &Analyzer{}
	for _, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("extra pattern %q: %w", p, err)
		}
		a.extra = append(a.extra, re)
	}
	for _, p := range ignore {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("ignore pattern %q: %w", p, err)
		}
		a.ignore = append(a.ignore, re)
	}
	return a, nil
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// category of a level word.
type category int

const (
	catNone category = iota // no level found
	catOK                   // info / warn / debug: decisively not an error
	catError
	catFatal
)

func (c category) level() string {
	if c == catFatal {
		return "fatal"
	}
	return "error"
}

func catOfWord(w string) category {
	switch strings.ToLower(w) {
	case "error", "err", "severe", "e", "eror":
		return catError
	case "fatal", "panic", "critical", "crit", "emerg", "emergency", "alert", "dpanic", "f":
		return catFatal
	case "trace", "debug", "info", "notice", "warn", "warning", "i", "w", "d", "t", "information", "verbose":
		return catOK
	}
	return catNone
}

func catOfNumber(f float64) category {
	switch {
	case f >= 60:
		return catFatal
	case f >= 50:
		return catError
	}
	return catOK
}

var (
	jsonLevelKeys = []string{"level", "severity", "lvl", "levelname", "loglevel", "log_level", "log.level", "@l"}
	jsonMsgKeys   = []string{"msg", "message", "error", "err", "exception", "@m", "@mt"}

	logfmtLevelRe = regexp.MustCompile(`(?:^|\s)(?:level|lvl|severity)=["']?([A-Za-z]+)`)
	logfmtMsgRe   = regexp.MustCompile(`(?:^|\s)(?:msg|message)=("(?:[^"\\]|\\.)*"|\S+)`)
	klogRe        = regexp.MustCompile(`^([IWEF])\d{4} \d{2}:\d{2}:\d{2}(?:\.\d+)?\s+\d+ \S+?\] ?(.*)$`)

	lvlUpperRe    = regexp.MustCompile(`(?:^|[\s\[<(|:])(TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|ERR|FATAL|PANIC|CRITICAL|CRIT|SEVERE|EMERG|ALERT)(?:$|[\s\]>):|,])`)
	lvlBracketRe  = regexp.MustCompile(`[\[<(](?i:trace|debug|info|notice|warn|warning|error|err|fatal|panic|critical|crit|severe)[\]>)]`)
	lvlLeadWordRe = regexp.MustCompile(`^(?i:error|fatal|panic|critical|warn|warning|info|debug)\s*:`)

	panicRe      = regexp.MustCompile(`^(?:panic:|fatal error:|FATAL ERROR:|runtime: )`)
	tracebackRe  = regexp.MustCompile(`^Traceback \(most recent call last\):`)
	javaThreadRe = regexp.MustCompile(`^Exception in thread "[^"]*" `)
	uncaughtRe   = regexp.MustCompile(`^(?:Unhandled|Uncaught)\s*(?:exception|error|rejection|promise)`)
	nodeErrRe    = regexp.MustCompile(`^(?:[A-Z][A-Za-z]*)?Error: `)
	javaExcRe    = regexp.MustCompile(`^[A-Za-z_$][\w$]*(?:\.[\w$]+)*(?:Exception|Error|Throwable)(?::|$)`)
)

// classified is the result of looking at one line.
type classified struct {
	cat category
	// msg is the message used for the template ("" = use the line itself).
	msg string
}

// classify inspects a single (ANSI-free, trimmed) line.
func classify(t string) classified {
	if t == "" {
		return classified{}
	}
	if t[0] == '{' {
		if c, ok := classifyJSON(t); ok {
			return c
		}
	}
	if m := klogRe.FindStringSubmatch(t); m != nil {
		return classified{cat: catOfWord(m[1]), msg: m[2]}
	}
	core := t
	if loc := leadTsRe.FindStringIndex(core); loc != nil {
		core = core[loc[1]:]
	}
	if panicRe.MatchString(core) || javaThreadRe.MatchString(core) || tracebackRe.MatchString(core) || uncaughtRe.MatchString(core) {
		c := catError
		if panicRe.MatchString(core) {
			c = catFatal
		}
		return classified{cat: c, msg: core}
	}
	if m := logfmtLevelRe.FindStringSubmatch(t); m != nil {
		if cat := catOfWord(m[1]); cat != catNone {
			msg := ""
			if mm := logfmtMsgRe.FindStringSubmatch(t); mm != nil {
				msg = unquote(mm[1])
			}
			return classified{cat: cat, msg: msg}
		}
	}
	if cat, ok := plainLevel(t); ok {
		return classified{cat: cat}
	}
	if nodeErrRe.MatchString(core) || javaExcRe.MatchString(core) {
		return classified{cat: catError, msg: core}
	}
	return classified{}
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return strings.Trim(s, `"`)
	}
	return s
}

// plainLevel finds the first level word near the start of a plain-text line.
// The first one wins, so "INFO retry after ERROR" is not an error.
func plainLevel(t string) (category, bool) {
	zone := t
	if len(zone) > 120 {
		zone = zone[:120]
	}
	best, word := -1, ""
	consider := func(idx int, w string) {
		if idx >= 0 && (best < 0 || idx < best) {
			best, word = idx, w
		}
	}
	if m := lvlUpperRe.FindStringSubmatchIndex(zone); m != nil {
		consider(m[2], zone[m[2]:m[3]])
	}
	if m := lvlBracketRe.FindStringIndex(zone); m != nil {
		consider(m[0]+1, strings.Trim(zone[m[0]:m[1]], "[]<>()"))
	}
	if m := lvlLeadWordRe.FindStringIndex(zone); m != nil {
		consider(m[0], strings.TrimRight(zone[m[0]:m[1]], ": \t"))
	}
	if best < 0 {
		return catNone, false
	}
	cat := catOfWord(word)
	return cat, cat != catNone
}

func classifyJSON(t string) (classified, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(t), &obj); err != nil {
		return classified{}, false
	}
	cat := catNone
	for _, k := range jsonLevelKeys {
		switch v := obj[k].(type) {
		case string:
			cat = catOfWord(v)
		case float64:
			cat = catOfNumber(v)
		}
		if cat != catNone {
			break
		}
	}
	msg := ""
	for _, k := range jsonMsgKeys {
		if s, ok := obj[k].(string); ok && strings.TrimSpace(s) != "" {
			msg = s
			break
		}
	}
	return classified{cat: cat, msg: msg}, true
}

func prepare(line string) string {
	return strings.TrimSpace(StripANSI(strings.TrimRight(line, "\r\n")))
}

// IsHead reports whether line starts an error entry. Lines matching an ignore
// pattern never do; lines matching an extra pattern always do.
func (a *Analyzer) IsHead(line string) bool {
	t := prepare(line)
	if t == "" {
		return false
	}
	if a != nil && matchAny(a.ignore, t) {
		return false
	}
	if c := classify(t); c.cat == catError || c.cat == catFatal {
		return true
	}
	return a != nil && matchAny(a.extra, t)
}

// Analyze builds the Entry of a group of lines (the first one is the head).
// ok is false when the entry is not an error after all or is ignored.
func (a *Analyzer) Analyze(lines []string) (Entry, bool) {
	if len(lines) == 0 {
		return Entry{}, false
	}
	prepared := make([]string, 0, len(lines))
	for _, l := range lines {
		prepared = append(prepared, strings.TrimRight(StripANSI(l), "\r\n"))
	}
	head := strings.TrimSpace(prepared[0])
	if head == "" {
		return Entry{}, false
	}
	for len(prepared) > 1 && strings.TrimSpace(prepared[len(prepared)-1]) == "" {
		prepared = prepared[:len(prepared)-1]
	}
	full := strings.Join(prepared, "\n")
	if a != nil && matchAny(a.ignore, full) {
		return Entry{}, false
	}
	c := classify(head)
	level := c.cat.level()
	if c.cat != catError && c.cat != catFatal {
		if a == nil || !matchAny(a.extra, head) {
			return Entry{}, false
		}
		level = "error"
	}

	msg := c.msg
	if msg == "" {
		msg = head
	}
	// A Python traceback is named by its final exception line.
	core := head
	if loc := leadTsRe.FindStringIndex(core); loc != nil {
		core = core[loc[1]:]
	}
	if tracebackRe.MatchString(core) {
		for i := len(prepared) - 1; i > 0; i-- {
			if l := strings.TrimSpace(prepared[i]); l != "" && prepared[i][0] != ' ' && prepared[i][0] != '\t' {
				msg = l
				break
			}
		}
	}
	msg = javaThreadRe.ReplaceAllString(msg, "")

	template := Normalize(Mask(msg))
	if template == "" {
		template = "error"
	}
	sample := Mask(head)
	if r := []rune(sample); len(r) > MaxSampleLen {
		sample = string(r[:MaxSampleLen])
	}
	message := Mask(full)
	if len(message) > MaxMessageLen {
		message = strings.ToValidUTF8(message[:MaxMessageLen], "")
	}
	return Entry{Level: level, Template: template, Sample: sample, Message: message}, true
}
