package covers

import (
	"fmt"
	"io"
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
			gameID, sourceURL, err := w.nextJob()
			if err != nil || gameID == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// Acquire a slot; blocks when all maxConcurrentDownloads are busy.
			w.sem <- struct{}{}
			go func(id int64, url string) {
				defer func() { <-w.sem }()
				w.downloadAndSave(id, url)
			}(gameID, sourceURL)
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
func (w *Worker) nextJob() (int64, string, error) {
	var gameID int64
	var sourceURL string

	// Prefer a game that's already in someone's library.
	// INNER JOIN is fast because library_items is small.
	err := w.db.QueryRow(`
		SELECT cj.game_id, cj.source_url
		FROM cover_jobs cj
		INNER JOIN library_items li ON li.game_id = cj.game_id
		WHERE cj.attempts < 5 AND cj.next_attempt_at <= ?
		ORDER BY cj.created_at ASC LIMIT 1`,
		time.Now().Format(time.RFC3339)).Scan(&gameID, &sourceURL)

	if err != nil {
		// sql.ErrNoRows or other error; return 0 to signal idle.
		return 0, "", nil
	}

	// Reserve the job for 30 minutes so the coordinator loop skips it.
	w.db.Exec("UPDATE cover_jobs SET next_attempt_at = ? WHERE game_id = ?",
		time.Now().Add(30*time.Minute).Format(time.RFC3339), gameID)
	return gameID, sourceURL, nil
}

// downloadAndSave fetches a cover image and writes it to disk, then updates
// the DB. On failure it resets the job with exponential backoff.
func (w *Worker) downloadAndSave(gameID int64, sourceURL string) {
	destPath := CoverPath(w.coverDir, gameID)
	if _, err := os.Stat(destPath); err == nil {
		// File already on disk — just mark the job complete.
		w.db.Exec("DELETE FROM cover_jobs WHERE game_id = ?", gameID)
		w.db.Exec("UPDATE games SET local_cover_path = ? WHERE id = ?", publicCoverPath(gameID), gameID)
		return
	}

	// Download the original URL (JPEG format).
	src, err := downloadCover(w.client, sourceURL)
	if err != nil {
		w.db.Exec(`UPDATE cover_jobs SET attempts = attempts + 1,
			last_error = ?, next_attempt_at = ?, updated_at = CURRENT_TIMESTAMP
			WHERE game_id = ?`, err.Error(), backoffNext(time.Now(), 0), gameID)
		return
	}
	defer src.Close()

	if err := os.MkdirAll(w.coverDir, 0755); err != nil {
		return
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return
	}
	defer dst.Close()

	io.Copy(dst, src)

	w.db.Exec("DELETE FROM cover_jobs WHERE game_id = ?", gameID)
	w.db.Exec("UPDATE games SET local_cover_path = ? WHERE id = ?", publicCoverPath(gameID), gameID)
}

func downloadCover(client *http.Client, url string) (io.ReadCloser, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func backoffNext(now time.Time, attempt int) string {
	if attempt >= 5 {
		return ""
	}
	d := time.Duration(1<<uint(attempt)) * time.Minute
	return now.Add(d).Format(time.RFC3339)
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
