package covers

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxConcurrentDownloads controls how many cover images are fetched in parallel.
// The IGDB image CDN (Cloudinary) has no documented per-client rate limit on image
// downloads, only on API queries — so we can safely fetch several covers at once.
const maxConcurrentDownloads = 5

type Worker struct {
	db       *sql.DB
	coverDir string
	client   *http.Client
	sem      chan struct{}
}

func NewWorker(db *sql.DB, coverDir string) *Worker {
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
// Two separate queries are used deliberately:
//  1. Library-priority query: INNER JOIN against the small library_items table
//     so SQLite only considers O(|library|) rows — no full-table sort.
//  2. Fallback query: no ORDER BY, so the idx_cover_jobs_next_attempt index
//     returns one row in O(1) without a temporary B-tree sort on all rows.
//
// A single LEFT JOIN query looks clean but forces a temp B-tree sort of the
// entire cover_jobs table (potentially 100k+ rows), which holds the single
// DB connection for 1–3 s and starves every concurrent HTTP handler.
func (w *Worker) nextJob() (int64, string, error) {
	var gameID int64
	var sourceURL string

	// Step 1: prefer a game that's already in someone's library.
	// INNER JOIN is fast because library_items is small.
	err := w.db.QueryRow(`
		SELECT cj.game_id, cj.source_url
		FROM cover_jobs cj
		INNER JOIN library_items li ON li.game_id = cj.game_id
		WHERE cj.attempts < 5 AND cj.next_attempt_at <= ?
		ORDER BY cj.created_at ASC LIMIT 1`,
		time.Now().Format(time.RFC3339)).Scan(&gameID, &sourceURL)

	if err == sql.ErrNoRows {
		// Step 2: no library games are waiting; grab any pending job.
		// Omitting ORDER BY lets SQLite return the first index entry it
		// finds without building a sort tree — O(1) with the index.
		err = w.db.QueryRow(`
			SELECT game_id, source_url FROM cover_jobs
			WHERE attempts < 5 AND next_attempt_at <= ?
			LIMIT 1`,
			time.Now().Format(time.RFC3339)).Scan(&gameID, &sourceURL)
	}

	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
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

	// Request WebP format from the IGDB CDN for a smaller payload.
	// Cloudinary (which powers images.igdb.com) serves WebP automatically
	// when the URL extension is changed from .jpg to .webp.
	webpURL := toWebPURL(sourceURL)

	src, err := downloadCover(w.client, webpURL)
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

// toWebPURL converts an IGDB Cloudinary URL from JPEG to WebP.
// Example: .../t_cover_big/co1wyy.jpg → .../t_cover_big/co1wyy.webp
func toWebPURL(rawURL string) string {
	if strings.HasSuffix(rawURL, ".jpg") {
		return rawURL[:len(rawURL)-4] + ".webp"
	}
	return rawURL
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
	return filepath.Join(coverDir, fmt.Sprintf("%d.webp", gameID))
}

// publicCoverPath returns the URL path served to the browser.
func publicCoverPath(gameID int64) string {
	return fmt.Sprintf("/covers/%d.webp", gameID)
}

// CoverExists reports whether a game's cover has been downloaded locally.
func CoverExists(coverDir string, gameID int64) bool {
	_, err := os.Stat(CoverPath(coverDir, gameID))
	return err == nil
}

// ServeCover handles GET /covers/... requests.
// It serves the local file if present, redirects to the IGDB CDN URL if not,
// and falls back to an inline SVG placeholder. Both .jpg and .webp filenames
// are accepted so the handler works with covers downloaded before the WebP
// migration and with any existing local_cover_path values in the DB.
func ServeCover(db *sql.DB, coverDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/covers/")
		if filename == "" || filename == "placeholder.jpg" {
			servePlaceholder(w)
			return
		}

		diskPath := filepath.Join(coverDir, filename)
		if _, err := os.Stat(diskPath); err == nil {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, diskPath)
			return
		}

		// Strip extension to parse the game ID — support both .jpg (legacy)
		// and .webp (current).
		idStr := filename
		for _, ext := range []string{".webp", ".jpg"} {
			if strings.HasSuffix(idStr, ext) {
				idStr = strings.TrimSuffix(idStr, ext)
				break
			}
		}
		gameID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			servePlaceholder(w)
			return
		}

		var coverURL string
		err = db.QueryRow("SELECT cover_url FROM games WHERE id = ?", gameID).Scan(&coverURL)
		if err != nil || coverURL == "" {
			servePlaceholder(w)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.Redirect(w, r, coverURL, http.StatusFound)
	}
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="264" height="374" viewBox="0 0 264 374">
<rect width="264" height="374" fill="#16213e"/>
<text x="132" y="187" font-family="sans-serif" font-size="16" fill="#999" text-anchor="middle" dominant-baseline="middle">No Cover</text>
</svg>`))
}
