package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cato/internal/db"
)

func TestRunCommands(t *testing.T) {
	ctx := context.Background()
	var out, errOut bytes.Buffer
	if err := run(ctx, []string{"help"}, &out, &errOut); err != nil || !strings.Contains(out.String(), "backfill KIND") {
		t.Errorf("help: %v %q", err, out.String())
	}
	if err := run(ctx, []string{"frobnicate"}, &out, &errOut); err == nil {
		t.Error("unknown command accepted")
	}
	if err := run(ctx, []string{"import-games"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("import without input: %v", err)
	}
	t.Setenv("IGDB_CLIENT_ID", "")
	t.Setenv("TWITCH_OAUTH_ID", "")
	// The legacy spelling still works and still needs IGDB credentials.
	if err := run(ctx, []string{"backfill-aliases"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "requires IGDB_CLIENT_ID") {
		t.Errorf("backfill without IGDB: %v", err)
	}
	if err := run(ctx, []string{"backfill", "everything"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "unknown backfill") {
		t.Errorf("unknown backfill kind: %v", err)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serve upgrades an old database (snapshotting it first), answers
// /healthz, and shuts down cleanly when its context ends.
func TestServeUpgradesAndShutsDown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "cato.db")
	os.MkdirAll(filepath.Dir(dbPath), 0o755)
	old, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateTo(old, 17); err != nil {
		t.Fatal(err)
	}
	old.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com')`)
	old.Close()

	static := filepath.Join(dir, "static")
	os.MkdirAll(static, 0o755)
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<!doctype html><title>Cato</title>"), 0o644)
	for k, v := range map[string]string{
		"CATO_DB_PATH": dbPath, "CATO_LISTEN_ADDR": "127.0.0.1:0", "CATO_ALLOW_INSECURE": "1",
		"CATO_STATIC_DIR": static, "CATO_COVER_DIR": filepath.Join(dir, "covers"), "CATO_BACKUP_DIR": "",
		"IGDB_CLIENT_ID": "", "TWITCH_OAUTH_ID": "", "GOOGLE_KEY": "", "CATO_DEV_TLS": "", "CATO_BASE_URL": "",
		"CATO_TLS_CERT_FILE": "", "CATO_TLS_KEY_FILE": "",
	} {
		t.Setenv(k, v)
	}

	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, io.Discard, logs) }()

	addr := regexp.MustCompile(`url=http://(\S+)`)
	var base string
	deadline := time.Now().Add(10 * time.Second)
	for base == "" {
		if m := addr.FindStringSubmatch(logs.String()); m != nil {
			base = "http://" + m[1]
		} else if time.Now().After(deadline) {
			t.Fatalf("server never listened; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("healthz %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v; logs:\n%s", err, logs.String())
		}
	case <-time.After(25 * time.Second):
		t.Fatal("serve did not shut down")
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "data", "backups", "cato-pre-v*-from-v17-*.db"))
	if len(snaps) != 1 {
		t.Errorf("pre-migration snapshots %v; logs:\n%s", snaps, logs.String())
	}
	if !strings.Contains(logs.String(), "signup=closed") || strings.Contains(logs.String(), "secret") {
		t.Errorf("startup config log:\n%s", logs.String())
	}
}
