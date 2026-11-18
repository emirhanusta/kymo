package auth

import (
	"strings"
	"sync"
	"time"
)

// Login rate limit: Max failed attempts per key within Window. Keys for a
// client IP ("ip:...") get a larger budget, since many users can share one IP
// (office NAT, a proxy); a single account is protected by its own login key.
const (
	LoginMaxAttempts      = 10
	LoginMaxAttemptsPerIP = 50
	LoginWindow           = 15 * time.Minute
)

// Limiter is an in-memory sliding-window failure counter keyed by string
// (e.g. "ip:1.2.3.4", "login:alice"). It is safe for concurrent use.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
	sweep  time.Time
}

// NewLimiter allows max failures per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// prune drops expired hits of key and returns the live ones. Caller holds mu.
func (l *Limiter) prune(key string, now time.Time) []time.Time {
	hs := l.hits[key]
	cut := now.Add(-l.window)
	i := 0
	for i < len(hs) && !hs[i].After(cut) {
		i++
	}
	hs = hs[i:]
	if len(hs) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = hs
	return hs
}

// Blocked reports whether key has used its budget, and how long until a slot frees.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	hs := l.prune(key, now)
	if len(hs) < l.budget(key) {
		return false, 0
	}
	return true, hs[0].Add(l.window).Sub(now)
}

// budget is the failure allowance of key: IP keys get the larger per-IP budget
// (never below the base one).
func (l *Limiter) budget(key string) int {
	if strings.HasPrefix(key, "ip:") && LoginMaxAttemptsPerIP > l.max {
		return l.max * LoginMaxAttemptsPerIP / LoginMaxAttempts
	}
	return l.max
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(key, now)
	l.hits[key] = append(l.hits[key], now)
}

// Reset forgets key (after a successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// sweepLocked removes idle keys at most once a minute so memory stays bounded.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.sweep) < time.Minute {
		return
	}
	l.sweep = now
	for k := range l.hits {
		l.prune(k, now)
	}
}
