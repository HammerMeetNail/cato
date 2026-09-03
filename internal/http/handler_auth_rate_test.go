package http

import (
	"testing"

	"cato/internal/config"
)

// CATO_AUTH_RATE_LIMIT must actually reach the per-IP limiters; a previous
// revision parsed the env var but NewAuthHandler ignored it (dead config).
func TestNewAuthHandlerHonorsAuthRateLimit(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	h := NewAuthHandler(database, &config.Config{AuthRateLimit: 2})
	for i := 0; i < 2; i++ {
		if !h.signupLimiter.Allow("9.9.9.9") {
			t.Fatalf("request %d blocked with limit 2", i+1)
		}
	}
	if h.signupLimiter.Allow("9.9.9.9") {
		t.Error("3rd request passed with limit 2")
	}

	def := NewAuthHandler(database, &config.Config{})
	for i := 0; i < 5; i++ {
		if !def.signupLimiter.Allow("8.8.8.8") {
			t.Fatalf("default request %d blocked (want limit 5)", i+1)
		}
	}
	if def.signupLimiter.Allow("8.8.8.8") {
		t.Error("6th request passed with default limit 5")
	}
}
