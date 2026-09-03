package igdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newLocalClient spins up an httptest API server returning the given games
// payload and returns a pre-authenticated client pointed at it (rate limiter
// disabled, token seeded — these tests exercise the API call, not OAuth).
func newLocalClient(t *testing.T, gamesPayload string) (*Client, *httptest.Server, *int64) {
	t.Helper()
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gamesPayload)
	}))
	t.Cleanup(srv.Close)
	c := preAuthedClient(t, srv.URL+"/")
	return c, srv, &requests
}

// preAuthedClient builds a test client that skips the token dance by seeding
// a long-lived access token.
func preAuthedClient(t *testing.T, apiBase string) *Client {
	t.Helper()
	c := newTestClient("id", "secret", apiBase, "http://127.0.0.1:1/unreachable")
	c.accessToken = "tok"
	c.tokenExpiry = time.Now().Add(time.Hour)
	return c
}

func TestSearchGamesBuildsBodyAndDecodes(t *testing.T) {
	c, _, reqs := newLocalClient(t, `[
		{"id":1,"name":"Zelda","cover":{"id":9,"image_id":"co9"},
		 "alternative_names":[{"id":1,"name":"botw"}],"follows":10,"total_rating_count":20,"game_type":0}
	]`)
	games, err := c.SearchGames(context.Background(), "zelda", 10, false)
	if err != nil {
		t.Fatalf("SearchGames: %v", err)
	}
	if len(games) != 1 || games[0].Name != "Zelda" {
		t.Fatalf("got %+v", games)
	}
	if games[0].CoverURL != "https://images.igdb.com/igdb/image/upload/t_cover_big/co9.jpg" {
		t.Errorf("CoverURL = %q", games[0].CoverURL)
	}
	if len(games[0].Aliases) != 1 || games[0].Aliases[0] != "botw" {
		t.Errorf("Aliases = %v", games[0].Aliases)
	}
	if atomic.LoadInt64(reqs) != 1 {
		t.Errorf("expected 1 API request, got %d", *reqs)
	}
}

// The search box feeds the APIQL `search "..."` clause; quotes must be
// escaped so a crafted query cannot alter the query structure.
func TestSearchGamesEscapesQuotes(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()
	c := preAuthedClient(t, srv.URL+"/")

	if _, err := c.SearchGames(context.Background(), `x"; fields url; --`, 5, false); err != nil {
		t.Fatalf("SearchGames: %v", err)
	}
	if strings.Contains(body, `x";`) {
		t.Errorf("query not escaped, body: %q", body)
	}
	if !strings.Contains(body, `search "x\" fields url --";`) {
		t.Errorf("unexpected escaped body: %q", body)
	}
}

func TestSearchGamesEmptyClientShortCircuits(t *testing.T) {
	c := NewClient("", "")
	games, err := c.SearchGames(context.Background(), "zelda", 10, false)
	if games != nil || err != nil {
		t.Errorf("expected (nil, nil) for unconfigured client, got (%v, %v)", games, err)
	}
}

func TestSearchGamesEditionFilters(t *testing.T) {
	cases := []struct {
		name            string
		query           string
		includeEditions bool
		wantVersionCond bool
		wantGameType    bool
	}{
		{"plain hides both", "zelda", false, true, true},
		{"edition keyword bypasses version filter", "zelda goty", false, false, true},
		{"pack keyword bypasses game_type filter", "zelda skin", false, true, false},
		{"includeEditions bypasses all", "zelda", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, 4096)
				n, _ := r.Body.Read(buf)
				body = string(buf[:n])
				fmt.Fprint(w, "[]")
			}))
			defer srv.Close()
			c := preAuthedClient(t, srv.URL+"/")
			if _, err := c.SearchGames(context.Background(), tc.query, 5, tc.includeEditions); err != nil {
				t.Fatalf("SearchGames: %v", err)
			}
			if got := strings.Contains(body, "version_parent = null"); got != tc.wantVersionCond {
				t.Errorf("version_parent cond = %v (body %q)", got, body)
			}
			if got := strings.Contains(body, "game_type ="); got != tc.wantGameType {
				t.Errorf("game_type cond = %v (body %q)", got, body)
			}
		})
	}
}

func TestGetGameFoundAndMissing(t *testing.T) {
	c, _, _ := newLocalClient(t, `[{"id":42,"name":"Minecraft"}]`)
	g, err := c.GetGame(context.Background(), 42)
	if err != nil || g == nil || g.Name != "Minecraft" {
		t.Fatalf("GetGame = (%v, %v)", g, err)
	}

	cEmpty, _, _ := newLocalClient(t, `[]`)
	g2, err := cEmpty.GetGame(context.Background(), 999)
	if err != nil || g2 != nil {
		t.Fatalf("expected (nil, nil) for missing game, got (%v, %v)", g2, err)
	}

	cNone := NewClient("", "")
	g3, err := cNone.GetGame(context.Background(), 1)
	if g3 != nil || err != nil {
		t.Errorf("unconfigured client: expected (nil, nil), got (%v, %v)", g3, err)
	}
}

func TestGetGamesBatch(t *testing.T) {
	c, _, _ := newLocalClient(t, `[{"id":1,"name":"A"},{"id":2,"name":"B"}]`)
	games, err := c.GetGamesBatch(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("GetGamesBatch: %v", err)
	}
	if len(games) != 2 {
		t.Fatalf("got %d games", len(games))
	}

	// Empty and oversize inputs.
	if g, err := (&Client{}).GetGamesBatch(context.Background(), nil); g != nil || err != nil {
		t.Errorf("empty ids: got (%v, %v)", g, err)
	}
	big := make([]int64, maxBatchIDs+1)
	if _, err := c.GetGamesBatch(context.Background(), big); err == nil {
		t.Error("expected error for batch > 500 ids")
	}
}

func TestGetPlatforms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":6,"name":"PC","abbreviation":"win"}]`)
	}))
	defer srv.Close()
	c := preAuthedClient(t, srv.URL+"/")
	plats, err := c.GetPlatforms(context.Background())
	if err != nil || len(plats) != 1 || plats[0].Name != "PC" {
		t.Fatalf("GetPlatforms = (%v, %v)", plats, err)
	}
	cNone := NewClient("", "")
	if p, err := cNone.GetPlatforms(context.Background()); p != nil || err != nil {
		t.Errorf("unconfigured: got (%v, %v)", p, err)
	}
}

func TestAuthenticateFetchesAndCachesToken(t *testing.T) {
	var tokenReqs int64
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&tokenReqs, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.PostForm.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		fmt.Fprint(w, `{"access_token":"tok123","expires_in":3600}`)
	}))
	defer authSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Client-ID") != "test-id" {
			t.Errorf("Client-ID = %q", r.Header.Get("Client-ID"))
		}
		fmt.Fprint(w, "[]")
	}))
	defer apiSrv.Close()

	c := newTestClient("test-id", "test-secret", apiSrv.URL+"/", authSrv.URL)
	if _, err := c.GetGame(context.Background(), 1); err != nil {
		t.Fatalf("GetGame: %v", err)
	}
	if _, err := c.GetGame(context.Background(), 2); err != nil {
		t.Fatalf("GetGame (cached token): %v", err)
	}
	if n := atomic.LoadInt64(&tokenReqs); n != 1 {
		t.Errorf("expected 1 token request (cached after), got %d", n)
	}
}

func TestAuthenticateFailure(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad credentials", http.StatusBadRequest)
	}))
	defer authSrv.Close()
	c := newTestClient("id", "secret", authSrv.URL+"/", authSrv.URL)
	if _, err := c.GetGame(context.Background(), 1); err == nil {
		t.Error("expected auth failure to propagate")
	}
}

func TestDoErrorStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"rate_limited", http.StatusTooManyRequests},
		{"server_error", http.StatusInternalServerError},
		{"bad_request", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			c := preAuthedClient(t, srv.URL+"/")
			if _, err := c.do(context.Background(), "games", "fields id;"); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// A 401 from the API must clear the cached token so the next call re-auths.
func TestDoUnauthorizedClearsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := preAuthedClient(t, srv.URL+"/")
	c.accessToken = "stale"
	if _, err := c.do(context.Background(), "games", "fields id;"); err == nil {
		t.Fatal("expected error")
	}
	c.mu.Lock()
	still := c.accessToken
	c.mu.Unlock()
	if still != "" {
		t.Errorf("token not cleared, still %q", still)
	}
}

func TestPostDecodesGames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":7,"name":"Portal"}]`)
	}))
	defer srv.Close()
	c := preAuthedClient(t, srv.URL+"/")
	games, err := c.post(context.Background(), "games", "fields id;")
	if err != nil || len(games) != 1 || games[0].ID != 7 {
		t.Fatalf("post = (%v, %v)", games, err)
	}

	// Invalid JSON must error, not return garbage.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer badSrv.Close()
	c2 := preAuthedClient(t, badSrv.URL+"/")
	if _, err := c2.post(context.Background(), "games", "fields id;"); err == nil {
		t.Error("expected decode error")
	}
}

func TestAuthenticateNoSecretShortCircuit(t *testing.T) {
	c := NewClient("id", "")
	if err := c.authenticate(context.Background()); err != nil {
		t.Errorf("authenticate without secret: %v", err)
	}
}

func TestGameCategoryPrefersGameType(t *testing.T) {
	if got := gameCategory(igdbGame{Category: 0, GameType: 13}); got != 13 {
		t.Errorf("gameType = %d, want 13", got)
	}
	if got := gameCategory(igdbGame{Category: 2, GameType: 0}); got != 2 {
		t.Errorf("category = %d, want 2", got)
	}
}

func TestIntsToJSON(t *testing.T) {
	if got := intsToJSON(nil); got != "[]" {
		t.Errorf("nil = %q", got)
	}
	if got := intsToJSON([]int64{6, 130}); got != "[6,130]" {
		t.Errorf("got %q", got)
	}
}

// The search handler normalizes the query before it reaches the client, but
// the client must still be safe for any caller.
func TestEscapeAPIQLString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{`he said "hi"`, `he said \"hi\"`},
		{`back\slash`, `back\\slash`},
		{"semi;colon", "semicolon"},
	}
	for _, tc := range cases {
		if got := escapeAPIQLString(tc.in); got != tc.want {
			t.Errorf("escapeAPIQLString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
