package auth

import (
	"testing"
	"time"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("mysecretpassword")
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	if hash == "" {
		t.Error("expected non-empty hash")
	}

	if !CheckPassword("mysecretpassword", hash) {
		t.Error("expected password to match")
	}

	if CheckPassword("wrongpassword", hash) {
		t.Error("expected wrong password to not match")
	}
}

func TestHashPasswordEmpty(t *testing.T) {
	hash, err := HashPassword("")
	if err != nil {
		t.Fatalf("failed to hash empty password: %v", err)
	}
	if hash == "" {
		t.Error("expected non-empty hash for empty password")
	}
}

func TestCheckPasswordEmptyHash(t *testing.T) {
	if CheckPassword("anything", "") {
		t.Error("expected empty hash to not match")
	}
}

func TestRandomToken(t *testing.T) {
	t1 := RandomToken(16)
	t2 := RandomToken(16)

	if t1 == "" {
		t.Error("expected non-empty token")
	}
	if len(t1) != 32 {
		t.Errorf("expected 32 hex chars (16 bytes), got %d", len(t1))
	}
	if t1 == t2 {
		t.Error("expected different random tokens")
	}
}

func TestHashToken(t *testing.T) {
	h := hashToken("test-token")
	if h == "" {
		t.Error("expected non-empty hash")
	}
	if len(h) != 64 {
		t.Errorf("expected 64 hex chars (SHA-256), got %d", len(h))
	}

	// Deterministic
	h2 := hashToken("test-token")
	if h != h2 {
		t.Error("expected deterministic hash")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	if !rl.Allow("key1") {
		t.Error("expected first request to be allowed")
	}
	if !rl.Allow("key1") {
		t.Error("expected second request to be allowed")
	}
	if !rl.Allow("key1") {
		t.Error("expected third request to be allowed")
	}
	if rl.Allow("key1") {
		t.Error("expected fourth request to be denied")
	}

	// Different key should still be allowed
	if !rl.Allow("key2") {
		t.Error("expected different key to be allowed")
	}
}

func TestRateLimiterWindow(t *testing.T) {
	rl := NewRateLimiter(1, time.Millisecond)

	if !rl.Allow("key") {
		t.Error("expected first request to be allowed")
	}
	if rl.Allow("key") {
		t.Error("expected second request to be denied")
	}
}
