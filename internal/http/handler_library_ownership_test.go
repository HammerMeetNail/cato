package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cato/internal/auth"
)

// getLibraryItem sends an authenticated GET for a single library item.
func getLibraryItem(t *testing.T, mux *http.ServeMux, db auth.Querier, sessionID string, gameID int) (map[string]interface{}, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/library/"+strconv.Itoa(gameID), nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp, rec.Code
}

func TestOwnershipFieldsRoundTrip(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Give game 1 a platforms list to verify it reaches the client.
	database.Exec(`UPDATE games SET platforms_json = '["PC (Microsoft Windows)","Nintendo Switch"]' WHERE id = 1`)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1,
		`{"status":"backlog","platform":"Nintendo Switch","medium":"physical"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d %s", rec.Code, rec.Body.String())
	}

	resp, code := getLibraryItem(t, mux, database, sessionID, 1)
	if code != http.StatusOK {
		t.Fatalf("GET failed: %d %s", code, resp)
	}
	if resp["platform"] != "Nintendo Switch" || resp["medium"] != "physical" {
		t.Errorf("GET platform=%v medium=%v", resp["platform"], resp["medium"])
	}
	plats := resp["platforms"].([]interface{})
	if len(plats) != 2 {
		t.Errorf("platforms = %v, want 2 entries from games.platforms_json", plats)
	}

	var platform, medium string
	database.QueryRow(`SELECT platform, medium FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&platform, &medium)
	if platform != "Nintendo Switch" || medium != "physical" {
		t.Errorf("stored platform=%q medium=%q", platform, medium)
	}
}

func TestPatchPreservesOwnershipWhenAbsent(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	postLibraryItem(t, mux, database, sessionID, 1,
		`{"status":"backlog","platform":"PC (Microsoft Windows)","medium":"digital"}`)

	// Status-only patch must not touch ownership.
	rec, _ := patchLibraryItem(t, mux, database, sessionID, 1, `{"status":"playing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH failed: %d %s", rec.Code, rec.Body.String())
	}

	var platform, medium string
	database.QueryRow(`SELECT platform, medium FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&platform, &medium)
	if platform != "PC (Microsoft Windows)" || medium != "digital" {
		t.Errorf("ownership lost on status patch: platform=%q medium=%q", platform, medium)
	}
}

func TestPatchCanClearOwnership(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog","platform":"PC","medium":"digital"}`)

	rec, _ := patchLibraryItem(t, mux, database, sessionID, 1, `{"platform":"","medium":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH failed: %d %s", rec.Code, rec.Body.String())
	}

	var platform, medium string
	database.QueryRow(`SELECT platform, medium FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&platform, &medium)
	if platform != "" || medium != "" {
		t.Errorf("expected cleared ownership, got platform=%q medium=%q", platform, medium)
	}
}

func TestUpsertRejectsInvalidMediumAndLongPlatform(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog","medium":"pirated"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid medium, got %d", rec.Code)
	}

	longPlatform := `{"status":"backlog","platform":"` + strings.Repeat("x", 65) + `"}`
	rec, _ = postLibraryItem(t, mux, database, sessionID, 1, longPlatform)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for overlong platform, got %d", rec.Code)
	}

	rec, _ = postLibraryItem(t, mux, database, sessionID, 2, `{"status":"backlog"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}
	rec, _ = patchLibraryItem(t, mux, database, sessionID, 2, `{"medium":"rented"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid patched medium, got %d", rec.Code)
	}
}
