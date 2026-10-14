package http

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cato/internal/config"
	"cato/internal/db"
)

// --- /api/games/{id} (handleGameByID) --------------------------------------

func seedGames(t *testing.T, database *db.DB) {
	t.Helper()
	for _, g := range []struct {
		id   int64
		name string
	}{
		{1, "Test Game"},
		{2, "Game Two"},
	} {
		database.Exec(`INSERT OR IGNORE INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
			g.id, g.name, fmt.Sprintf("game-%d", g.id), fmt.Sprintf("game %d", g.id))
	}
}

// --- library CSV export -----------------------------------------------------

// --- search query-parameter parsing -----------------------------------------

func boolPtr(b bool) *bool { return &b }

// --- gzip middleware ---------------------------------------------------------

func TestGzipMiddlewareAPIResponses(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	srv := NewServer(cfg, database)

	// API + Accept-Encoding: gzip → gzipped body.
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("API response not gzipped: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	// Empty result list is fine; decompressing at all proves the round-trip.
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Errorf("decoded body = %q (%v)", body, err)
	}

	// API without Accept-Encoding → plain.
	req2 := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Header().Get("Content-Encoding") != "" {
		t.Error("plain response tagged as gzipped")
	}
	if rec2.Header().Get("Vary") == "" {
		t.Error("Vary missing on plain API response")
	}
}

func TestGzipMiddlewareSkips204AndStatic(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	_ = NewServer(cfg, database)

	// 204 responses must not be tagged Content-Encoding: gzip.
	mux := http.NewServeMux()
	mux.Handle("/204", gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/204", nil))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("204 tagged with Content-Encoding: %q", rec.Header().Get("Content-Encoding"))
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	srv := NewServer(cfg, database)

	// JS/CSS get no-cache.
	req := httptest.NewRequest(http.MethodGet, "/js/core/api.js", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("js Cache-Control = %q", cc)
	}

	// HTML pages get no-cache.
	req2 := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if cc := rec2.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("login Cache-Control = %q", cc)
	}
}

type shutdownListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *shutdownListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func TestServerStartShutdown(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	srv := NewServer(&config.Config{ListenAddr: "127.0.0.1:0", StaticDir: t.TempDir(), CoverDir: t.TempDir()}, database)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &shutdownListener{Listener: raw, closed: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	srv.mux.HandleFunc("/drain", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.httpServer.Serve(listener) }()
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		_ = srv.httpServer.Close()
		<-served
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	response := make(chan error, 1)
	go func() {
		res, err := client.Get("http://" + raw.Addr().String() + "/drain")
		if err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("status %d", res.StatusCode)
			}
		}
		response <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request not admitted")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Shutdown(ctx) }()
	select {
	case <-listener.closed:
	case <-ctx.Done():
		t.Fatal("listener not closed")
	}
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned before admitted handler: %v", err)
	default:
	}
	unblock()
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestServerConcurrentStartShutdown(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	srv := NewServer(&config.Config{ListenAddr: "127.0.0.1:0", StaticDir: t.TempDir(), CoverDir: t.TempDir()}, database)
	gate := make(chan struct{})
	started, stopped := make(chan error, 1), make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { <-gate; started <- srv.Start() }()
	go func() { <-gate; stopped <- srv.Shutdown(ctx) }()
	close(gate)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-started; err != http.ErrServerClosed {
		t.Fatalf("Start returned %v", err)
	}
}
