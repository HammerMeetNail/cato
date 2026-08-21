package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

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

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Key by client IP (host only — RemoteAddr includes the port, which
		// would give every TCP connection a fresh bucket and make the limit
		// meaningless). X-Forwarded-For is deliberately NOT trusted: it is
		// client-controlled and would let anyone rotate identities to bypass
		// the limit.
		key := r.RemoteAddr
		if host, _, err := net.SplitHostPort(key); err == nil {
			key = host
		}
		if !rl.Allow(key) {
			writeJSON(w, http.StatusTooManyRequests, errResp("rate_limited", "Too many requests. Please try again later."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
