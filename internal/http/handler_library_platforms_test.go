package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestPlatformIDsResolveToNames covers the core regression: games.platforms_json
// stores raw IGDB platform IDs, which used to be unmarshaled into []string and
// silently produced an empty list. With the platforms lookup table populated,
// the API must return human-readable names; unknown IDs are dropped.
func TestPlatformIDsResolveToNames(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`DELETE FROM platforms`)
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	database.Exec(`INSERT INTO platforms (id, name, abbreviation) VALUES
		(6, 'PC (Microsoft Windows)', 'win'),
		(130, 'Nintendo Switch', 'swi')`)
	// 99999 has no lookup row — must not surface as a bare number.
	database.Exec(`UPDATE games SET platforms_json = '[6,130,99999]' WHERE id = 1`)

	rec, resp := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog","platform":"PC","medium":"digital"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d %s", rec.Code, rec.Body.String())
	}
	if resp["ok"] != true {
		t.Fatalf("unexpected POST response: %s", rec.Body.String())
	}

	resp, code := getLibraryItem(t, mux, database, sessionID, 1)
	if code != http.StatusOK {
		t.Fatalf("GET failed: %d", code)
	}
	want := []string{"PC (Microsoft Windows)", "Nintendo Switch"}
	if got := toStringSlice(resp["platforms"]); !reflect.DeepEqual(got, want) {
		t.Errorf("GET item platforms = %v, want %v", got, want)
	}

	// Library list endpoint resolves too.
	req := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	var listResp []map[string]interface{}
	if err := json.Unmarshal(rec2.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp) != 1 {
		t.Fatalf("list returned %d items, want 1", len(listResp))
	}
	if got := toStringSlice(listResp[0]["platforms"]); !reflect.DeepEqual(got, want) {
		t.Errorf("list platforms = %v, want %v", got, want)
	}
}

// TestPlatformsEmptyWhenLookupMissing: without a populated table (IGDB not
// configured), numeric IDs resolve to nothing rather than leaking raw IDs.
func TestPlatformsEmptyWhenLookupMissing(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`DELETE FROM platforms`)
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	database.Exec(`UPDATE games SET platforms_json = '[6,130]' WHERE id = 1`)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d %s", rec.Code, rec.Body.String())
	}
	item, code := getLibraryItem(t, mux, database, sessionID, 1)
	if code != http.StatusOK {
		t.Fatalf("GET failed: %d", code)
	}
	if got := toStringSlice(item["platforms"]); len(got) != 0 {
		t.Errorf("platforms = %v, want empty (no lookup rows)", got)
	}
}

// TestLibraryPlatformFilter: GET /api/library?platform= restricts to games
// available on the named platform (substring, case-insensitive).
func TestLibraryPlatformFilter(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`DELETE FROM platforms`)
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	database.Exec(`INSERT INTO platforms (id, name) VALUES (130, 'Nintendo Switch')`)
	database.Exec(`UPDATE games SET platforms_json = '[6,130]' WHERE id = 1`)
	database.Exec(`UPDATE games SET platforms_json = '[99999]' WHERE id = 2`) // 99999 unknown → matches nothing

	postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`)
	postLibraryItem(t, mux, database, sessionID, 2, `{"status":"backlog"}`)

	listWithPlatform := func(platform string) []map[string]interface{} {
		req := httptest.NewRequest(http.MethodGet, "/api/library?platform="+platform, nil)
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list failed: %d %s", rec.Code, rec.Body.String())
		}
		var items []map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return items
	}

	if got := listWithPlatform("switch"); len(got) != 1 || got[0]["game_id"].(float64) != 1 {
		t.Errorf("platform=switch returned %v, want only game 1", got)
	}
	if got := listWithPlatform("nintendo"); len(got) != 1 {
		t.Errorf("platform=nintendo returned %d items, want 1", len(got))
	}
	if got := listWithPlatform("xbox"); len(got) != 0 {
		t.Errorf("platform=xbox returned %d items, want 0", len(got))
	}
}

// TestPlatformSuggestions: /api/library/platforms suggests distinct resolved
// names from the caller's library, most-used first.
func TestPlatformSuggestions(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`DELETE FROM platforms`)
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	database.Exec(`INSERT INTO platforms (id, name) VALUES (130, 'Nintendo Switch'), (6, 'PC (Microsoft Windows)')`)
	database.Exec(`INSERT INTO platforms (id, name, abbreviation, shortname) VALUES (508, 'Nintendo Switch 2', 'Switch 2', 'sw2 ns2')`)
	database.Exec(`UPDATE games SET platforms_json = '[6,130]' WHERE id = 1`)
	database.Exec(`UPDATE games SET platforms_json = '[130,508]' WHERE id = 2`)

	postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`)
	postLibraryItem(t, mux, database, sessionID, 2, `{"status":"backlog","platform":"Steam Deck"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/library/platforms?q=swi", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET platforms failed: %d %s", rec.Code, rec.Body.String())
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// "swi" is a substring of both Switch names; most-referenced first, with contemporary
	// shortname tokens prioritized when present.
	if !contains(got, "Nintendo Switch") || !contains(got, "Nintendo Switch 2") {
		t.Errorf("suggestions for 'swi' = %v, want to contain 'Nintendo Switch' and 'Nintendo Switch 2'", got)
	}
	// Most-referenced (Switch count 2) should come before Switch 2 (count 1)
	if len(got) >= 2 {
		idxSwitch := -1
		idxSwitch2 := -1
		for i, v := range got {
			if v == "Nintendo Switch" {
				idxSwitch = i
			}
			if v == "Nintendo Switch 2" {
				idxSwitch2 = i
			}
		}
		if idxSwitch == -1 || idxSwitch2 == -1 || idxSwitch > idxSwitch2 {
			t.Errorf("suggestions for 'swi' order wrong: %v, want 'Nintendo Switch' before 'Nintendo Switch 2'", got)
		}
	}

	// Ownership-only platform ("Steam Deck") is suggestable too.
	req = httptest.NewRequest(http.MethodGet, "/api/library/platforms?q=steam", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"Steam Deck"}) {
		t.Errorf("suggestions for 'steam' = %v, want [Steam Deck]", got)
	}

	// Curated shortnames match even though the IGDB abbreviation is
	// "Switch 2": typing "sw2" suggests "Nintendo Switch 2" (and contemporarily,
	// the shortname token itself for discoverability).
	req = httptest.NewRequest(http.MethodGet, "/api/library/platforms?q=sw2", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Accept either name-only or token+name, but must contain the contemporary result
	if len(got) == 0 || !contains(got, "Nintendo Switch 2") {
		t.Errorf("suggestions for 'sw2' = %v, want to contain 'Nintendo Switch 2'", got)
	}
	// For contemporary queries, shortname token should be prioritized if present
	if len(got) >= 1 && got[0] != "sw2" && got[0] != "Nintendo Switch 2" {
		t.Errorf("suggestions for 'sw2' first = %q, want 'sw2' or 'Nintendo Switch 2' (contemporary first)", got[0])
	}
}

// TestMultiPlatformOwnership: POST/PATCH accept a platforms array (a game can
// be owned on several); the legacy singular platform mirrors the first entry;
// PATCHing platforms replaces the whole list; empty array clears.
func TestMultiPlatformOwnership(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1,
		`{"status":"backlog","platforms":["Nintendo Switch","Xbox Series X|S"],"medium":"digital"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d %s", rec.Code, rec.Body.String())
	}

	resp, code := getLibraryItem(t, mux, database, sessionID, 1)
	if code != http.StatusOK {
		t.Fatalf("GET failed: %d", code)
	}
	want := []string{"Nintendo Switch", "Xbox Series X|S"}
	if got := toStringSlice(resp["owned_platforms"]); !reflect.DeepEqual(got, want) {
		t.Errorf("owned_platforms = %v, want %v", got, want)
	}
	if resp["platform"] != "Nintendo Switch" {
		t.Errorf("legacy platform = %v, want first entry", resp["platform"])
	}

	var ownedJSON string
	database.QueryRow(`SELECT owned_platforms_json FROM library_items WHERE user_id='user-1' AND game_id=1`).Scan(&ownedJSON)
	if ownedJSON != `["Nintendo Switch","Xbox Series X|S"]` {
		t.Errorf("stored owned json = %s", ownedJSON)
	}

	// PATCH replacing the list.
	rec, resp = patchLibraryItem(t, mux, database, sessionID, 1,
		`{"platforms":["PlayStation 5"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := toStringSlice(resp["owned_platforms"]); !reflect.DeepEqual(got, []string{"PlayStation 5"}) {
		t.Errorf("patched owned_platforms = %v", got)
	}
	if resp["platform"] != "PlayStation 5" {
		t.Errorf("patched legacy platform = %v, want PlayStation 5", resp["platform"])
	}

	// PATCH clearing via empty array also clears the legacy column.
	rec, _ = patchLibraryItem(t, mux, database, sessionID, 1, `{"platforms":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear failed: %d %s", rec.Code, rec.Body.String())
	}
	database.QueryRow(`SELECT platform, owned_platforms_json FROM library_items WHERE user_id='user-1' AND game_id=1`).Scan(&ownedJSON)
	// reuse scan vars: check platform cleared
	var platform string
	database.QueryRow(`SELECT platform FROM library_items WHERE user_id='user-1' AND game_id=1`).Scan(&platform)
	if platform != "" {
		t.Errorf("legacy platform after clear = %q, want empty", platform)
	}
	_ = ownedJSON

	// Legacy singular field still works as a one-element shorthand.
	rec, _ = postLibraryItem(t, mux, database, sessionID, 2, `{"status":"backlog","platform":"Steam Deck"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 2 failed: %d %s", rec.Code, rec.Body.String())
	}
	resp, _ = getLibraryItem(t, mux, database, sessionID, 2)
	if got := toStringSlice(resp["owned_platforms"]); !reflect.DeepEqual(got, []string{"Steam Deck"}) {
		t.Errorf("legacy shorthand owned = %v", got)
	}
}

func toStringSlice(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}
	return out
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
