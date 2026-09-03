package covers

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cato/internal/db"
)

// maxConcurrentDownloads controls how many cover images are fetched in parallel.
// The IGDB image CDN (Cloudinary) has no documented per-client rate limit on image
// downloads, only on API queries — so we can safely fetch several covers at once.
const maxConcurrentDownloads = 5

// maxCoverBytes caps how large a downloaded cover may be. Real cover art is
// well under 1 MB; the cap only exists so a misbehaving server can't make us
// buffer unbounded data.
const maxCoverBytes = 8 << 20 // 8 MiB

type Worker struct {
	db       *db.DB
	coverDir string
	client   *http.Client
	sem      chan struct{}
}

func NewWorker(db *db.DB, coverDir string) *Worker {
	return &Worker{
		db:       db,
		coverDir: coverDir,
		client:   &http.Client{Timeout: 30 * time.Second},
		sem:      make(chan struct{}, maxConcurrentDownloads),
	}
}

// Start cleans up any stale DB paths then runs a coordinator goroutine that
// picks pending cover jobs and dispatches them to download goroutines.
// Up to maxConcurrentDownloads downloads run in parallel; when there are no
// pending jobs the coordinator sleeps briefly before polling again.
func (w *Worker) Start() {
	w.CleanStaleLocalPaths()
		go func() {
		for {
			gameID, sourceURL, attempts, err := w.nextJob()
			if err != nil || gameID == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// Acquire a slot; blocks when all maxConcurrentDownloads are busy.
			w.sem <- struct{}{}
			go func(id int64, url string, atts int) {
				defer func() { <-w.sem }()
				w.downloadAndSave(id, url, atts)
			}(gameID, sourceURL, attempts)
			// Yield the DB connection between job claims so HTTP request
			// handlers (session lookups, library queries) are not starved
			// by rapid back-to-back cover_jobs writes.
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// CleanStaleLocalPaths removes local_cover_path values from the DB for any
// game whose cover file no longer exists on disk. It reads the stored path
// from the DB rather than reconstructing it, so it handles both .jpg and
// .webp files correctly.
func (w *Worker) CleanStaleLocalPaths() {
	rows, err := w.db.Query("SELECT id, local_cover_path FROM games WHERE local_cover_path != ''")
	if err != nil {
		return
	}
	defer rows.Close()

	var cleaned int
	for rows.Next() {
		var id int64
		var localPath string
		if err := rows.Scan(&id, &localPath); err != nil {
			continue
		}
		// localPath is a URL path like "/covers/123.webp"; derive the disk path.
		diskPath := filepath.Join(w.coverDir, filepath.Base(localPath))
		if _, err := os.Stat(diskPath); err != nil {
			w.db.Exec("UPDATE games SET local_cover_path = '' WHERE id = ?", id)
			cleaned++
		}
	}
	if cleaned > 0 {
		fmt.Printf("covers: cleared %d stale local_cover_path values\n", cleaned)
	}
}

// nextJob selects the highest-priority pending cover job and immediately
// "claims" it by pushing its next_attempt_at far into the future. This
// prevents the coordinator loop from re-selecting the same job while its
// download goroutine is still running.
//
// Only library-priority jobs are downloaded. The query INNER JOINs against
// the library_items table so SQLite only considers O(|library|) rows.
// When there are no library jobs, the worker idles (returns gameID == 0).
func (w *Worker) nextJob() (int64, string, int, error) {
	var gameID int64
	var sourceURL string
	var attempts int

	// Prefer a game that's already in someone's library.
	// INNER JOIN is fast because library_items is small.
	//
	// Timestamps here mix SQLite's CURRENT_TIMESTAMP ("YYYY-MM-DD HH:MM:SS",
	// always UTC) with Go-formatted RFC3339. Both sides must be UTC: SQLite's
	// space separator sorts before RFC3339's 'T', so same-instant values
	// compare correctly, but a local-time RFC3339 string is offset from UTC
	// and shifts every comparison by the host's UTC offset. Use utcNow().
	err := w.db.QueryRow(`
		SELECT cj.game_id, cj.source_url, cj.attempts
		FROM cover_jobs cj
		INNER JOIN library_items li ON li.game_id = cj.game_id
		WHERE cj.attempts < 5 AND cj.next_attempt_at <= ?
		ORDER BY cj.created_at ASC LIMIT 1`,
		utcNow()).Scan(&gameID, &sourceURL, &attempts)

	if err != nil {
		// sql.ErrNoRows or other error; return 0 to signal idle.
		return 0, "", 0, nil
	}

	// Reserve the job for 30 minutes so the coordinator loop skips it.
	w.db.Exec("UPDATE cover_jobs SET next_attempt_at = ? WHERE game_id = ?",
		time.Now().UTC().Add(30*time.Minute).Format(time.RFC3339), gameID)
	return gameID, sourceURL, attempts, nil
}

// utcNow returns the current UTC time as an RFC3339 string, comparable with
// SQLite CURRENT_TIMESTAMP defaults (see nextJob).
func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// downloadAndSave fetches a cover image and writes it to disk, then updates
// the DB. On failure it records the attempt and schedules a retry with real
// exponential backoff (1m, 2m, 4m, 8m, 16m).
func (w *Worker) downloadAndSave(gameID int64, sourceURL string, attempts int) {
	destPath := CoverPath(w.coverDir, gameID)
	if _, err := os.Stat(destPath); err == nil {
		// File already on disk — just mark the job complete.
		w.db.Exec("DELETE FROM cover_jobs WHERE game_id = ?", gameID)
		w.db.Exec("UPDATE games SET local_cover_path = ? WHERE id = ?", publicCoverPath(gameID), gameID)
		return
	}

	data, err := fetchCover(w.client, sourceURL)
	if err != nil {
		w.recordFailure(gameID, attempts+1, err)
		return
	}

	if err := w.saveAtomically(destPath, data); err != nil {
		w.recordFailure(gameID, attempts+1, fmt.Errorf("save: %w", err))
		return
	}

	w.db.Exec("DELETE FROM cover_jobs WHERE game_id = ?", gameID)
	w.db.Exec("UPDATE games SET local_cover_path = ? WHERE id = ?", publicCoverPath(gameID), gameID)
}

// recordFailure bumps the attempt counter and schedules the next try with
// exponential backoff based on the actual attempt number. (The previous code
// always passed attempt=0 here, collapsing the "backoff" to a flat 1 minute.)
func (w *Worker) recordFailure(gameID int64, attempt int, err error) {
	log.Printf("covers: game %d failed (attempt %d): %v", gameID, attempt, err)
	w.db.Exec(`UPDATE cover_jobs SET attempts = ?, last_error = ?,
		next_attempt_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE game_id = ?`, attempt, truncateErr(err), backoffNext(time.Now(), attempt), gameID)
}

// saveAtomically writes data to destPath via a temp file + rename so a crash
// mid-write can never leave a truncated {id}.jpg behind — such a file would
// pass the os.Stat fast-path on the next attempt and be served with immutable
// caching forever.
func (w *Worker) saveAtomically(destPath string, data []byte) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cover-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, destPath)
}

// fetchCover downloads a cover image, enforcing an HTTP 200 status, a size
// cap, and image magic bytes. Validating before anything touches disk stops
// truncated responses or HTML error pages from being cached as {id}.jpg.
func fetchCover(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > maxCoverBytes {
		return nil, fmt.Errorf("image exceeds %d byte limit", maxCoverBytes)
	}
	if !looksLikeImage(data) {
		n := len(data)
		if n > 4 {
			n = 4
		}
		return nil, fmt.Errorf("response is not an image (magic bytes: % x)", data[:n])
	}
	return data, nil
}

// looksLikeImage reports whether b starts with a known image magic number.
// IGDB serves JPEG; WEBP/PNG are accepted for legacy sources.
func looksLikeImage(b []byte) bool {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF: // JPEG
		return true
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return true
	case len(b) >= 8 && b[0] == 0x89 && string(b[1:4]) == "PNG":
		return true
	}
	return false
}

// truncateErr caps an error string before storing it in cover_jobs.last_error,
// which has no length limit but shouldn't hold multi-KB DNS error dumps.
func truncateErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return s
}

func backoffNext(now time.Time, attempt int) string {
	if attempt >= 5 {
		return ""
	}
	d := time.Duration(1<<uint(attempt)) * time.Minute
	return now.UTC().Add(d).Format(time.RFC3339)
}

// CoverPath returns the on-disk path for a game's locally cached cover.
func CoverPath(coverDir string, gameID int64) string {
	return filepath.Join(coverDir, fmt.Sprintf("%d.jpg", gameID))
}

// publicCoverPath returns the URL path served to the browser.
func publicCoverPath(gameID int64) string {
	return fmt.Sprintf("/covers/%d.jpg", gameID)
}

// CoverExists reports whether a game's cover has been downloaded locally.
// It checks for .jpg (current) or .webp (legacy files).
func CoverExists(coverDir string, gameID int64) bool {
	jpgPath := filepath.Join(coverDir, fmt.Sprintf("%d.jpg", gameID))
	if _, err := os.Stat(jpgPath); err == nil {
		return true
	}
	webpPath := filepath.Join(coverDir, fmt.Sprintf("%d.webp", gameID))
	if _, err := os.Stat(webpPath); err == nil {
		return true
	}
	return false
}

// ServeCover handles GET /covers/... requests.
// It serves the local file if present (with long cache headers) or a placeholder
// (with short cache headers) if not. No DB query or redirect; instantly returns
// from disk or a cached SVG placeholder.
func ServeCover(coverDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/covers/")
		if filename == "" || filename == "placeholder.jpg" {
			servePlaceholder(w, 300) // short cache on miss
			return
		}

		// Strip extension to parse the game ID — support both .jpg and .webp.
		idStr := filename
		for _, ext := range []string{".jpg", ".webp"} {
			if strings.HasSuffix(idStr, ext) {
				idStr = strings.TrimSuffix(idStr, ext)
				break
			}
		}
		gameID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			servePlaceholder(w, 300)
			return
		}

		// Look on disk for .jpg then .webp (prefer .jpg, support legacy .webp).
		var diskPath string
		for _, ext := range []string{".jpg", ".webp"} {
			candidate := filepath.Join(coverDir, fmt.Sprintf("%d%s", gameID, ext))
			if _, err := os.Stat(candidate); err == nil {
				diskPath = candidate
				break
			}
		}

		if diskPath != "" {
			// Hit: serve from disk with long-term cache (1 year, immutable).
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFile(w, r, diskPath)
			return
		}

		// Miss: serve placeholder with short cache so browser retries soon.
		servePlaceholder(w, 300)
	}
}

func servePlaceholder(w http.ResponseWriter, maxAge int) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="264" height="374" viewBox="0 0 264 374">
<rect width="264" height="374" fill="#16213e"/>
<text x="132" y="187" font-family="sans-serif" font-size="16" fill="#999" text-anchor="middle" dominant-baseline="middle">No Cover</text>
</svg>`))
}
