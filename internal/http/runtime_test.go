package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cato/internal/config"
	"cato/internal/db"
)

func TestAPIBodyLimit(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		for _, size := range []int{maxAPIBodyBytes, maxAPIBodyBytes + 1} {
			called := false
			h := securityMiddleware(apiBodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				data, _ := io.ReadAll(r.Body)
				if len(data) != size {
					t.Errorf("body length = %d", len(data))
				}
				w.WriteHeader(http.StatusNoContent)
			})))
			req := httptest.NewRequest("POST", "/api/test", strings.NewReader(strings.Repeat("x", size)))
			if chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if size > maxAPIBodyBytes {
				if called || rr.Code != 413 || !strings.Contains(rr.Body.String(), `"request_too_large"`) {
					t.Fatalf("overflow: called=%v status=%d body=%s", called, rr.Code, rr.Body)
				}
				if rr.Header().Get("Content-Type") != "application/json" {
					t.Fatal("non-JSON overflow")
				}
			} else if !called || rr.Code != 204 {
				t.Fatalf("boundary: called=%v status=%d", called, rr.Code)
			}
			if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Content-Type-Options") != "nosniff" || rr.Header().Get("X-Frame-Options") != "DENY" {
				t.Fatal("missing response protections")
			}
		}
	}
}

func TestRuntimeHealthAndShutdownBeforeStart(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	srv := NewServer(&config.Config{ListenAddr: "127.0.0.1:0", StaticDir: t.TempDir()}, database)
	if srv.httpServer.ReadHeaderTimeout <= 0 || srv.httpServer.ReadTimeout <= 0 || srv.httpServer.WriteTimeout <= 0 || srv.httpServer.IdleTimeout <= 0 {
		t.Fatal("missing timeouts")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	// Holding the sole writer proves health checks the writer and observes cancellation.
	conn, err := database.Write.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil).WithContext(ctx))
	if rr.Code != 503 {
		t.Fatal(rr.Code)
	}
	conn.Close()
	database.Write.Close()
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 503 {
		t.Fatal("closed writer accepted")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != http.ErrServerClosed {
		t.Fatalf("Start after Shutdown: %v", err)
	}
}

func TestHealthWriterDeadline(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn, err := database.Write.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	srv := &Server{db: database}
	finished := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.handleHealthz(rr, httptest.NewRequest("GET", "/healthz", nil))
		finished <- rr.Code
	}()
	select {
	case status := <-finished:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("health status = %d", status)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("health check exceeded its deadline")
	}
}
