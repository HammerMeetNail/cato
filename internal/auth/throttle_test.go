package auth

import (
	"testing"
	"time"
)

func TestLoginThrottleExponentialBackoff(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lt := NewLoginThrottle()
	lt.Now = func() time.Time { return now }
	key := "a@example.com"
	for i := 0; i < 5; i++ {
		if wait := lt.Wait(key); wait != 0 {
			t.Fatalf("attempt %d blocked for %v", i, wait)
		}
		lt.Failure(key)
	}
	// 5 failures: 1s, then doubling, capped at 15 minutes.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for _, w := range want {
		if got := lt.Wait(key); got != w {
			t.Fatalf("wait = %v, want %v", got, w)
		}
		now = now.Add(w)
		if got := lt.Wait(key); got != 0 {
			t.Fatalf("still blocked after %v: %v", w, got)
		}
		lt.Failure(key)
	}
	for i := 0; i < 30; i++ {
		lt.Failure(key)
	}
	if got := lt.Wait(key); got != 15*time.Minute {
		t.Fatalf("cap = %v", got)
	}
	lt.Success(key)
	if got := lt.Wait(key); got != 0 {
		t.Fatalf("success did not reset: %v", got)
	}
}

func TestLoginThrottleForgetsIdleAccounts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lt := NewLoginThrottle()
	lt.Now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		lt.Failure(string(rune('a'+i%26)) + string(rune(i)))
	}
	now = now.Add(25 * time.Hour)
	lt.Failure("fresh")
	if n := lt.Len(); n != 1 {
		t.Fatalf("idle entries kept: %d", n)
	}
}

func TestRateLimiterEvictsIdleKeys(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(5, time.Minute)
	rl.Now = func() time.Time { return now }
	for i := 0; i < 500; i++ {
		rl.Allow(string(rune(0x4e00 + i)))
	}
	if rl.Len() != 500 {
		t.Fatalf("len = %d", rl.Len())
	}
	now = now.Add(2 * time.Minute)
	rl.Allow("new")
	if n := rl.Len(); n != 1 {
		t.Fatalf("idle keys not evicted: %d", n)
	}
}
