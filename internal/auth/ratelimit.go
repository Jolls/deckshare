package auth

import (
	"sync"
	"time"
)

// limiter is a hand-rolled fixed-window counter, keyed by an arbitrary string (an IP, an email,
// etc.). Per-process and in-memory is correct for Phase 1's single-instance deployment
// (architecture.md §12).
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time // injectable for tests
	seen   map[string]*bucket
}

type bucket struct {
	count   int
	resetAt time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{
		limit:  limit,
		window: window,
		now:    time.Now,
		seen:   make(map[string]*bucket),
	}
}

// Allow reports whether key is under budget, incrementing its counter either way. When it
// returns false, retryAfter is how long until the window resets.
func (l *limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, exists := l.seen[key]
	if !exists || now.After(b.resetAt) || now.Equal(b.resetAt) {
		b = &bucket{resetAt: now.Add(l.window)}
		l.seen[key] = b
	}
	b.count++
	if b.count > l.limit {
		return false, b.resetAt.Sub(now)
	}
	return true, 0
}

// Sweep deletes buckets whose window has already elapsed, bounding memory.
func (l *limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for key, b := range l.seen {
		if now.After(b.resetAt) {
			delete(l.seen, key)
		}
	}
}
