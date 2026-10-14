package http

import (
	"net/http"
	"sync"
	"testing"
)

// Regression (plan §2.1 #3): PATCH used to read the item from the read pool,
// add the delta in Go and write an absolute value, so two quick "+30m" taps
// could lose one. Concurrent deltas must all land.
func TestConcurrentPlaytimeDeltasAreNotLost(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"playing"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}

	const taps = 24
	var wg sync.WaitGroup
	codes := make(chan int, taps)
	for i := 0; i < taps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _ := patchLibraryItem(t, mux, database, sessionID, 1, `{"playtime_delta_minutes":30}`)
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("PATCH status %d", code)
		}
	}
	var minutes int64
	if err := database.QueryRow(`SELECT playtime_minutes FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if minutes != taps*30 {
		t.Fatalf("playtime = %d, want %d (lost updates)", minutes, taps*30)
	}
}

// A concurrent field PATCH must not overwrite a delta that committed between
// its read and its write (and vice versa).
func TestConcurrentFieldPatchKeepsPlaytimeDeltas(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"playing","notes":"start"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: %d", rec.Code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			patchLibraryItem(t, mux, database, sessionID, 1, `{"playtime_delta_minutes":15}`)
		}()
		go func() {
			defer wg.Done()
			patchLibraryItem(t, mux, database, sessionID, 1, `{"rating":80}`)
		}()
	}
	wg.Wait()
	var minutes, rating int64
	var notes string
	if err := database.QueryRow(`SELECT playtime_minutes, rating, notes FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&minutes, &rating, &notes); err != nil {
		t.Fatal(err)
	}
	if minutes != 12*15 || rating != 80 || notes != "start" {
		t.Fatalf("minutes=%d rating=%d notes=%q", minutes, rating, notes)
	}
}
