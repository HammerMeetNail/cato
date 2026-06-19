package covers

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Worker struct {
	db        *sql.DB
	coverDir  string
	client    *http.Client
	rateLimit chan struct{}
}

func NewWorker(db *sql.DB, coverDir string) *Worker {
	w := &Worker{
		db:        db,
		coverDir:  coverDir,
		client:    &http.Client{Timeout: 30 * time.Second},
		rateLimit: make(chan struct{}, 1),
	}
	return w
}

func (w *Worker) Start() {
	go func() {
		for {
			w.processNext()
			time.Sleep(time.Second)
		}
	}()
}

func (w *Worker) processNext() {
	// Acquire rate limit
	w.rateLimit <- struct{}{}
	defer func() { <-w.rateLimit }()

	gameID, sourceURL, err := w.nextJob()
	if err != nil || gameID == 0 {
		return
	}

	destPath := CoverPath(w.coverDir, gameID)
	if _, err := os.Stat(destPath); err == nil {
		// Cover already exists, mark as done
		w.db.Exec("DELETE FROM cover_jobs WHERE game_id = ?", gameID)
		w.db.Exec("UPDATE games SET local_cover_path = ? WHERE id = ?", publicCoverPath(gameID), gameID)
		return
	}

	src, err := downloadCover(w.client, sourceURL, gameID)
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

func (w *Worker) nextJob() (int64, string, error) {
	var gameID int64
	var sourceURL string
	err := w.db.QueryRow(`SELECT game_id, source_url FROM cover_jobs
		WHERE attempts < 5 AND next_attempt_at <= ?
		ORDER BY CASE WHEN game_id IN (SELECT DISTINCT game_id FROM library_items) THEN 0 ELSE 1 END,
		created_at ASC LIMIT 1`, time.Now().Format(time.RFC3339)).Scan(&gameID, &sourceURL)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	return gameID, sourceURL, err
}

func downloadCover(client *http.Client, url string, gameID int64) (io.ReadCloser, error) {
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

func CoverPath(coverDir string, gameID int64) string {
	return filepath.Join(coverDir, fmt.Sprintf("%d.jpg", gameID))
}

func publicCoverPath(gameID int64) string {
	return fmt.Sprintf("/covers/%d.jpg", gameID)
}

func CoverExists(coverDir string, gameID int64) bool {
	_, err := os.Stat(CoverPath(coverDir, gameID))
	return err == nil
}
