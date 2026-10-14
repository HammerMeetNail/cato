package igdb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubTransport answers token and API requests in-process without any shared
// state, so the only synchronization between goroutines is the client's own
// (a real http.Transport's connection-pool mutex would hide token races from
// the race detector).
type stubTransport struct{}

func (stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `[]`
	status := http.StatusOK
	if r.URL.Host == "auth" {
		body = `{"access_token":"tok","expires_in":1}` // expires at once: every call refreshes
	} else if r.Header.Get("Authorization") != "Bearer tok" {
		status = http.StatusUnauthorized
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

// S10: the token is read and refreshed concurrently (run under -race).
func TestConcurrentRequestsShareTokenSafely(t *testing.T) {
	c := newTestClient("id", "secret", "http://api/", "http://auth/token")
	c.httpClient = &http.Client{Transport: stubTransport{}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.do(context.Background(), "games", "fields id;"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

// S10: a 401 (token revoked/expired early) re-authenticates and retries once.
func TestUnauthorizedRetriesWithFreshToken(t *testing.T) {
	var issued atomic.Int64
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":"fresh-%d","expires_in":3600}`, issued.Add(1))
	}))
	defer auth.Close()
	var calls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `[{"id":1,"name":"Ok"}]`)
	}))
	defer api.Close()
	c := newTestClient("id", "secret", api.URL+"/", auth.URL)
	c.accessToken = "stale"
	c.tokenExpiry = time.Now().Add(time.Hour)
	games, err := c.post(context.Background(), "games", "fields id;")
	if err != nil || len(games) != 1 {
		t.Fatalf("post = %v, %v", games, err)
	}
	if calls.Load() != 2 || issued.Load() != 1 {
		t.Fatalf("api calls=%d tokens issued=%d", calls.Load(), issued.Load())
	}
}

// S10: 429 backs off (honouring Retry-After) and retries.
func TestTooManyRequestsBacksOffAndRetries(t *testing.T) {
	var calls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer api.Close()
	c := preAuthedClient(t, api.URL+"/")
	c.backoff = time.Millisecond
	if _, err := c.do(context.Background(), "games", "fields id;"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
	// Persistent 429s give up with an error instead of looping.
	var always atomic.Int64
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		always.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer busy.Close()
	c2 := preAuthedClient(t, busy.URL+"/")
	c2.backoff = time.Millisecond
	if _, err := c2.do(context.Background(), "games", "fields id;"); err == nil || always.Load() > 4 {
		t.Fatalf("err=%v calls=%d", err, always.Load())
	}
	// Backoff respects cancellation.
	c3 := preAuthedClient(t, busy.URL+"/")
	c3.backoff = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c3.do(ctx, "games", "fields id;"); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("cancelled backoff: err=%v after %v", err, time.Since(start))
	}
}

// S10: response bodies are bounded.
func TestResponseBodyIsBounded(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("["))
		chunk := []byte(strings.Repeat(`{"id":1},`, 1000))
		for written := 0; written < maxResponseBytes+len(chunk); written += len(chunk) {
			w.Write(chunk)
		}
	}))
	defer api.Close()
	c := preAuthedClient(t, api.URL+"/")
	if _, err := c.do(context.Background(), "games", "fields id;"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized body: %v", err)
	}
}
