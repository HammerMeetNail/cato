package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ListenAddr != ":7080" {
		t.Errorf("expected ListenAddr :7080, got %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "data/cato.db" {
		t.Errorf("expected DBPath data/cato.db, got %s", cfg.DBPath)
	}
	if cfg.StaticDir != "web/static" {
		t.Errorf("expected StaticDir web/static, got %s", cfg.StaticDir)
	}
	if cfg.CoverDir != "data/covers" {
		t.Errorf("expected CoverDir data/covers, got %s", cfg.CoverDir)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	os.Setenv("CATO_LISTEN_ADDR", ":9090")
	os.Setenv("CATO_DB_PATH", "/tmp/test.db")
	defer func() {
		os.Unsetenv("CATO_LISTEN_ADDR")
		os.Unsetenv("CATO_DB_PATH")
	}()

	cfg := Load()
	if cfg.ListenAddr != ":9090" {
		t.Errorf("expected ListenAddr :9090, got %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("expected DBPath /tmp/test.db, got %s", cfg.DBPath)
	}
}

func TestGetEnv(t *testing.T) {
	if got := getEnv("NONEXISTENT_VAR", "default"); got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}

	os.Setenv("CATO_TEST_VAR", "testvalue")
	defer os.Unsetenv("CATO_TEST_VAR")

	if got := getEnv("CATO_TEST_VAR", "default"); got != "testvalue" {
		t.Errorf("expected 'testvalue', got %q", got)
	}
}

func TestIntFromEnv(t *testing.T) {
	const key = "CATO_TEST_INT_VAR"
	defer os.Unsetenv(key)

	os.Unsetenv(key)
	if got := intFromEnv(key, 7); got != 7 {
		t.Errorf("unset: got %d, want 7", got)
	}
	for _, bad := range []string{"", "abc", "0", "-3"} {
		os.Setenv(key, bad)
		if got := intFromEnv(key, 7); got != 7 {
			t.Errorf("%q: got %d, want fallback 7", bad, got)
		}
	}
	os.Setenv(key, "42")
	if got := intFromEnv(key, 7); got != 42 {
		t.Errorf("42: got %d", got)
	}
}

func TestLoadAuthRateLimit(t *testing.T) {
	const key = "CATO_AUTH_RATE_LIMIT"
	old, had := os.LookupEnv(key)
	defer func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	}()

	os.Unsetenv(key)
	if got := Load().AuthRateLimit; got != 0 {
		t.Errorf("unset: got %d, want 0", got)
	}
	os.Setenv(key, "100")
	if got := Load().AuthRateLimit; got != 100 {
		t.Errorf("100: got %d", got)
	}
	os.Setenv(key, "bogus")
	if got := Load().AuthRateLimit; got != 0 {
		t.Errorf("bogus: got %d, want 0", got)
	}
}
