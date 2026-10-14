package auth

import (
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu        sync.Mutex
	attempts  map[string][]time.Time
	limit     int
	window    time.Duration
	lastSweep time.Time
	// Now is the clock; tests replace it.
	Now func() time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
		Now:      time.Now,
	}
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.Now()
	cutoff := now.Add(-rl.window)
	// Once per window, drop keys with no attempts left in it, so the map is
	// bounded by recent clients rather than every address ever seen.
	if now.Sub(rl.lastSweep) >= rl.window {
		rl.lastSweep = now
		for k, times := range rl.attempts {
			if len(times) == 0 || !times[len(times)-1].After(cutoff) {
				delete(rl.attempts, k)
			}
		}
	}

	recent := make([]time.Time, 0)
	for _, t := range rl.attempts[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	if len(recent) >= rl.limit {
		rl.attempts[key] = recent
		return false
	}

	recent = append(recent, now)
	rl.attempts[key] = recent
	return true
}

// Len reports how many keys are tracked.
func (rl *RateLimiter) Len() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.attempts)
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Key by client IP (host only — RemoteAddr includes the port, which
		// would give every TCP connection a fresh bucket and make the limit
		// meaningless). Forwarded headers count only from CATO_TRUSTED_PROXIES
		// (see ResolveClient); otherwise they are client-controlled.
		if !rl.Allow(ClientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, errResp("rate_limited", "Too many requests. Please try again later."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
