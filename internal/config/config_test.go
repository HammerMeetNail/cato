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
