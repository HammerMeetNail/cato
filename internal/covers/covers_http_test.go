package covers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cato/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// seedGame inserts a minimal game row so library_items / cover_jobs FKs hold.
func seedGame(t *testing.T, database *db.DB, id int64) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
		id, fmt.Sprintf("Game %d", id), fmt.Sprintf("game-%d", id), fmt.Sprintf("game %d", id))
	if err != nil {
		t.Fatalf("seed game: %v", err)
	}
}

func seedUserAndLibraryItem(t *testing.T, database *db.DB, userID string, gameID int64) {
	t.Helper()
	seedGame(t, database, gameID)
	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES (?, ?)`,
		userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO library_items (user_id, game_id, status) VALUES (?, ?, 'playing')`,
		userID, gameID); err != nil {
		t.Fatalf("seed library item: %v", err)
	}
}

func TestNextJobPicksLibraryJobOnly(t *testing.T) {
	database := testDB(t)
	w := NewWorker(database, t.TempDir())

	// A job with no library item must not be selected.
	seedGame(t, database, 100)
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (100, 'https://x/1.jpg')`); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	id, _, _, err := w.nextJob()
	if err != nil {
		t.Fatalf("nextJob: %v", err)
	}
	if id != 0 {
		t.Fatalf("non-library job was selected: %d", id)
	}

	// Adding a library item makes it selectable.
	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ('user-1', 'user-1@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO library_items (user_id, game_id, status) VALUES ('user-1', 100, 'playing')`); err != nil {
		t.Fatalf("seed library item: %v", err)
	}
	id, url, attempts, err := w.nextJob()
	if err != nil {
		t.Fatalf("nextJob: %v", err)
	}
	if id != 100 || url != "https://x/1.jpg" || attempts != 0 {
		t.Fatalf("nextJob = (%d, %q, %d)", id, url, attempts)
	}

	// The claim pushes next_attempt_at 30 minutes out, so the job is not
	// immediately re-selectable.
	id2, _, _, _ := w.nextJob()
	if id2 != 0 {
		t.Errorf("claimed job re-selected: %d", id2)
	}
}

func TestNextJobRespectsAttemptsCap(t *testing.T) {
	database := testDB(t)
	w := NewWorker(database, t.TempDir())
	seedUserAndLibraryItem(t, database, "user-1", 200)
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url, attempts) VALUES (200, 'u', 5)`); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id, _, _, _ := w.nextJob(); id != 0 {
		t.Errorf("exhausted job selected: %d", id)
	}
}

func TestDownloadAndSaveSuccess(t *testing.T) {
	database := testDB(t)
	coverDir := t.TempDir()
	w := NewWorker(database, coverDir)
	seedUserAndLibraryItem(t, database, "user-1", 300)

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpeg)
	}))
	defer srv.Close()

	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (300, ?)`, srv.URL); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w.downloadAndSave(300, srv.URL, 0)

	// File written, job removed, local_cover_path set.
	if !CoverExists(coverDir, 300) {
		t.Error("cover file missing after download")
	}
	if _, err := os.Stat(filepath.Join(coverDir, "300.jpg")); err != nil {
		t.Errorf("300.jpg not on disk: %v", err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 300`).Scan(&n); err != nil || n != 0 {
		t.Errorf("job not deleted (n=%d err=%v)", n, err)
	}
	var localPath string
	if err := database.QueryRow(`SELECT local_cover_path FROM games WHERE id = 300`).Scan(&localPath); err != nil || localPath != "/covers/300.jpg" {
		t.Errorf("local_cover_path = %q (err %v)", localPath, err)
	}
}

func TestDownloadAndSaveFailureRecordsAttempt(t *testing.T) {
	database := testDB(t)
	w := NewWorker(database, t.TempDir())
	seedUserAndLibraryItem(t, database, "user-1", 400)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (400, ?)`, srv.URL); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w.downloadAndSave(400, srv.URL, 0)

	var attempts int
	var lastErr string
	if err := database.QueryRow(`SELECT attempts, last_error FROM cover_jobs WHERE game_id = 400`).Scan(&attempts, &lastErr); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if lastErr == "" {
		t.Error("last_error empty after failure")
	}
}

func TestDownloadAndSaveExistingFileShortCircuits(t *testing.T) {
	database := testDB(t)
	coverDir := t.TempDir()
	w := NewWorker(database, coverDir)
	seedUserAndLibraryItem(t, database, "user-1", 500)

	// Pre-place the file; the download must be skipped entirely.
	if err := os.WriteFile(filepath.Join(coverDir, "500.jpg"), []byte("pretend-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Server that would fail the request if it were called.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w.downloadAndSave(500, srv.URL, 3)

	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 500`).Scan(&n); err != nil || n != 0 {
		t.Errorf("job not completed (n=%d err=%v)", n, err)
	}
}

func TestCleanStaleLocalPaths(t *testing.T) {
	database := testDB(t)
	coverDir := t.TempDir()
	w := NewWorker(database, coverDir)

	seedGame(t, database, 1)
	seedGame(t, database, 2)
	seedGame(t, database, 3)
	// Game 1 has a file on disk; game 2 does not; game 3 has a legacy webp on disk.
	if err := os.WriteFile(filepath.Join(coverDir, "1.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coverDir, "3.webp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   int64
		path string
	}{
		{1, "/covers/1.jpg"},
		{2, "/covers/2.jpg"},
		{3, "/covers/3.webp"},
	} {
		if _, err := database.Exec(`UPDATE games SET local_cover_path = ? WHERE id = ?`, tc.path, tc.id); err != nil {
			t.Fatal(err)
		}
	}

	w.CleanStaleLocalPaths()

	var g1, g2, g3 string
	database.QueryRow(`SELECT local_cover_path FROM games WHERE id = 1`).Scan(&g1)
	database.QueryRow(`SELECT local_cover_path FROM games WHERE id = 2`).Scan(&g2)
	database.QueryRow(`SELECT local_cover_path FROM games WHERE id = 3`).Scan(&g3)
	if g1 != "/covers/1.jpg" {
		t.Errorf("game 1 path cleared: %q", g1)
	}
	if g2 != "" {
		t.Errorf("game 2 stale path kept: %q", g2)
	}
	if g3 != "/covers/3.webp" {
		t.Errorf("game 3 legacy path cleared: %q", g3)
	}
}

func TestFetchCoverValidates(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpeg)
	}))
	defer srv.Close()

	data, err := fetchCover(srv.Client(), srv.URL)
	if err != nil || len(data) != len(jpeg) {
		t.Fatalf("fetchCover = (%d bytes, %v)", len(data), err)
	}

	// HTML masquerading as an image must be rejected.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not an image</html>")
	}))
	defer badSrv.Close()
	if _, err := fetchCover(badSrv.Client(), badSrv.URL); err == nil {
		t.Error("non-image body accepted")
	}
}

func TestTruncateErrEdgeCases(t *testing.T) {
	if got := truncateErr(fmt.Errorf("short")); got != "short" {
		t.Errorf("short error mangled: %q", got)
	}
	long := strings.Repeat("a", 400)
	got := truncateErr(fmt.Errorf("%s", long))
	if len(got) != 300 || !strings.HasSuffix(got, "...") {
		t.Errorf("long error not truncated: len=%d suffix=%q", len(got), got[len(got)-4:])
	}
}

func TestCoverPathHelpers(t *testing.T) {
	if got := CoverPath("/covers", 42); got != filepath.Join("/covers", "42.jpg") {
		t.Errorf("CoverPath = %q", got)
	}
	if got := publicCoverPath(42); got != "/covers/42.jpg" {
		t.Errorf("publicCoverPath = %q", got)
	}
	dir := t.TempDir()
	if CoverExists(dir, 7) {
		t.Error("CoverExists true for missing file")
	}
	os.WriteFile(filepath.Join(dir, "7.webp"), []byte("x"), 0o644)
	if !CoverExists(dir, 7) {
		t.Error("CoverExists false for legacy webp")
	}
}

func TestServeCoverHitAndMiss(t *testing.T) {
	dir := t.TempDir()
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01}
	os.WriteFile(filepath.Join(dir, "10.jpg"), jpeg, 0o644)

	// Hit: file content with immutable cache.
	req := httptest.NewRequest(http.MethodGet, "/covers/10.jpg", nil)
	rec := httptest.NewRecorder()
	ServeCover(dir)(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Bytes()[0] != 0xFF {
		t.Errorf("hit: code=%d body len=%d", rec.Code, rec.Body.Len())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hit cache-control = %q", cc)
	}

	// Miss: SVG placeholder with short cache.
	req2 := httptest.NewRequest(http.MethodGet, "/covers/99.jpg", nil)
	rec2 := httptest.NewRecorder()
	ServeCover(dir)(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Header().Get("Content-Type"), "svg") {
		t.Errorf("miss: code=%d ct=%q", rec2.Code, rec2.Header().Get("Content-Type"))
	}
	if cc := rec2.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=300") {
		t.Errorf("miss cache-control = %q", cc)
	}

	// Garbage path: placeholder.
	req3 := httptest.NewRequest(http.MethodGet, "/covers/notanumber.jpg", nil)
	rec3 := httptest.NewRecorder()
	ServeCover(dir)(rec3, req3)
	if !strings.Contains(rec3.Header().Get("Content-Type"), "svg") {
		t.Error("garbage id did not get placeholder")
	}

	// Bare /covers/ path: placeholder.
	req4 := httptest.NewRequest(http.MethodGet, "/covers/", nil)
	rec4 := httptest.NewRecorder()
	ServeCover(dir)(rec4, req4)
	if !strings.Contains(rec4.Header().Get("Content-Type"), "svg") {
		t.Error("empty id did not get placeholder")
	}

	// Legacy webp is served.
	os.WriteFile(filepath.Join(dir, "11.webp"), jpeg, 0o644)
	req5 := httptest.NewRequest(http.MethodGet, "/covers/11.webp", nil)
	rec5 := httptest.NewRecorder()
	ServeCover(dir)(rec5, req5)
	if rec5.Code != http.StatusOK || rec5.Body.Bytes()[0] != 0xFF {
		t.Errorf("webp hit: code=%d", rec5.Code)
	}
}

// The Start coordinator loop is exercised only indirectly (it loops forever);
// nextJob + downloadAndSave above cover its building blocks. A smoke pass on
// the sem-based dispatch is done via direct downloadAndSave concurrency.
func TestConcurrentDownloads(t *testing.T) {
	database := testDB(t)
	w := NewWorker(database, t.TempDir())
	seedUserAndLibraryItem(t, database, "user-1", 600)
	seedUserAndLibraryItem(t, database, "user-2", 601)

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Write(jpeg)
	}))
	defer srv.Close()

	for _, id := range []int64{600, 601} {
		database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (?, ?)`, id, srv.URL)
	}

	w.sem <- struct{}{}
	go func() { defer func() { <-w.sem }(); w.downloadAndSave(600, srv.URL, 0) }()
	w.sem <- struct{}{}
	go func() { defer func() { <-w.sem }(); w.downloadAndSave(601, srv.URL, 0) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id IN (600, 601)`).Scan(&n)
		if n == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error("jobs not completed within deadline")
}

var _ = context.Background
