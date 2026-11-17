package errorlog

import (
	"regexp"
	"strings"
	"time"
)

// Grouping limits.
const (
	// MaxGroupLines is the most lines of one entry (head + continuation) kept.
	MaxGroupLines = 50
	// GroupIdle closes a pending entry when no line arrived for this long.
	GroupIdle = 200 * time.Millisecond
)

// Group is a finished error entry: the head line plus its continuation lines.
type Group struct {
	Lines []string
	// Time is the log timestamp of the head line (zero when unknown).
	Time time.Time
	// Truncated is set when lines beyond MaxGroupLines were dropped.
	Truncated bool
}

type groupMode int

const (
	modePlain groupMode = iota
	modeGo
	modePython
)

var (
	dotsMoreRe  = regexp.MustCompile(`^\.\.\. \d+ (?:more|common frames omitted)`)
	goCallRe    = regexp.MustCompile(`^[\w./*()\-\[\]{}]+\(.*\)\s*$`)
	goExtraRe   = regexp.MustCompile(`^(?:goroutine \d+|created by |exit status |\[signal |panic: )`)
	pyChainRe   = regexp.MustCompile(`^(?:During handling of the above exception|The above exception was the direct cause)`)
	pyExcLineRe = regexp.MustCompile(`^[A-Za-z_][\w.]*(?::\s.*|:)?$`)
)

// Grouper assembles error entries from consecutive log lines of ONE container
// stream: stack-trace continuation lines are attached to their head line. It is
// not safe for concurrent use.
type Grouper struct {
	maxLines int
	idle     time.Duration

	cur   []string
	mode  groupMode
	pyExc bool // python: the exception line was seen
	trunc bool
	ts    time.Time
	last  time.Time
}

// NewGrouper returns a Grouper; zero arguments use MaxGroupLines / GroupIdle.
func NewGrouper(maxLines int, idle time.Duration) *Grouper {
	if maxLines <= 0 {
		maxLines = MaxGroupLines
	}
	if idle <= 0 {
		idle = GroupIdle
	}
	return &Grouper{maxLines: maxLines, idle: idle}
}

// Pending reports whether an entry is open.
func (g *Grouper) Pending() bool { return len(g.cur) > 0 }

func (g *Grouper) take() Group {
	out := Group{Lines: g.cur, Time: g.ts, Truncated: g.trunc}
	g.cur, g.mode, g.pyExc, g.trunc, g.ts = nil, modePlain, false, false, time.Time{}
	return out
}

// Add feeds one line (logTime is its log timestamp, now the receive time) and
// returns the entries completed by it (at most one).
func (g *Grouper) Add(a *Analyzer, line string, logTime, now time.Time) []Group {
	var out []Group
	if len(g.cur) > 0 {
		if g.continues(line) {
			if len(g.cur) < g.maxLines {
				g.cur = append(g.cur, strings.TrimRight(line, "\r\n"))
			} else {
				g.trunc = true
			}
			g.last = now
			return nil
		}
		out = append(out, g.take())
	}
	if a.IsHead(line) {
		g.cur = []string{strings.TrimRight(line, "\r\n")}
		g.ts, g.last = logTime, now
		core := prepare(line)
		if loc := leadTsRe.FindStringIndex(core); loc != nil {
			core = core[loc[1]:]
		}
		switch {
		case panicRe.MatchString(core):
			g.mode = modeGo
		case tracebackRe.MatchString(core):
			g.mode = modePython
		}
	}
	return out
}

// Idle closes the open entry when no line arrived for the idle duration.
func (g *Grouper) Idle(now time.Time) []Group {
	if len(g.cur) > 0 && now.Sub(g.last) >= g.idle {
		return []Group{g.take()}
	}
	return nil
}

// Flush closes the open entry unconditionally.
func (g *Grouper) Flush() []Group {
	if len(g.cur) > 0 {
		return []Group{g.take()}
	}
	return nil
}

// NextDeadline returns how long until the open entry goes idle (0 when none).
func (g *Grouper) NextDeadline(now time.Time) (time.Duration, bool) {
	if len(g.cur) == 0 {
		return 0, false
	}
	d := g.idle - now.Sub(g.last)
	if d < 0 {
		d = 0
	}
	return d, true
}

func (g *Grouper) continues(line string) bool {
	t := strings.TrimRight(line, "\r\n")
	blank := strings.TrimSpace(t) == ""
	if tracebackRe.MatchString(strings.TrimSpace(t)) {
		g.mode, g.pyExc = modePython, false
		return true
	}
	switch g.mode {
	case modeGo:
		if blank {
			return true
		}
		if t[0] == ' ' || t[0] == '\t' || goExtraRe.MatchString(t) || goCallRe.MatchString(t) {
			return true
		}
		return false
	case modePython:
		if blank {
			return true
		}
		if t[0] == ' ' || t[0] == '\t' {
			return true
		}
		if pyChainRe.MatchString(t) {
			g.pyExc = false
			return true
		}
		if !g.pyExc && pyExcLineRe.MatchString(strings.TrimSpace(t)) {
			g.pyExc = true
			return true
		}
		return false
	}
	if blank {
		return false
	}
	if t[0] == ' ' || t[0] == '\t' {
		return true
	}
	if strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "Caused by") || strings.HasPrefix(t, "Suppressed:") || dotsMoreRe.MatchString(t) {
		return true
	}
	// "ERROR Failed to process" followed by its exception class line.
	if len(g.cur) == 1 && javaExcRe.MatchString(t) {
		return true
	}
	return false
}
