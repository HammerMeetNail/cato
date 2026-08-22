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
