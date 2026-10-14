package http

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/db"
)

// avatarDir sits next to the covers (data/avatars).
func avatarDir(coverDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(coverDir)), "avatars")
}

// serveAvatars serves uploaded avatars to signed-in people only (nothing
// on this server is visible without an account). File names change with
// every upload, so they are cached as immutable, privately.
func serveAvatars(database *db.DB, cookies auth.Cookies, dir string) http.Handler {
	files := http.StripPrefix("/avatars/", http.FileServer(noListingFS{http.Dir(dir)}))
	return auth.AuthRequired(database, cookies)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/avatars/")
		if name == "" || strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".jpg") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	}))
}

// systemStatus feeds the admin System page: IGDB lanes, the cover queue,
// database and WAL size, the last backup, and job health.
func (s *Server) systemStatus(ctx context.Context) map[string]any {
	out := map[string]any{"asset_version": s.assets.Version(), "server_time": time.Now().UTC().Format(time.RFC3339)}
	size := func(p string) int64 {
		if st, err := os.Stat(p); err == nil {
			return st.Size()
		}
		return 0
	}
	out["db_bytes"] = size(s.cfg.DBPath)
	out["wal_bytes"] = size(s.cfg.DBPath + "-wal")
	var queued, failing int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(attempts > 0), 0) FROM cover_jobs`).Scan(&queued, &failing)
	out["cover_queue"] = map[string]int{"queued": queued, "retrying": failing}
	var users, games, items int
	s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM games), (SELECT COUNT(*) FROM library_items)`).Scan(&users, &games, &items)
	out["counts"] = map[string]int{"users": users, "games": games, "library_items": items}
	out["igdb_configured"] = s.cfg.IGDBClientID != ""
	out["igdb_lanes"] = s.catalog.LaneStatus()
	if s.cfg.BackupDir != "" {
		if entries, err := os.ReadDir(s.cfg.BackupDir); err == nil {
			var names []string
			for _, e := range entries {
				if !e.IsDir() {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			if len(names) > 0 {
				last := names[len(names)-1]
				b := map[string]any{"name": last, "count": len(names), "bytes": size(filepath.Join(s.cfg.BackupDir, last))}
				if st, err := os.Stat(filepath.Join(s.cfg.BackupDir, last)); err == nil {
					b["at"] = st.ModTime().UTC().Format(time.RFC3339)
				}
				out["last_backup"] = b
			}
		}
	}
	if s.jobs != nil {
		if sts, err := s.jobs.Statuses(ctx); err == nil {
			out["jobs"] = sts
		}
		lag, job := s.jobs.MaxLag(ctx)
		out["job_lag_seconds"] = int64(lag.Seconds())
		out["most_overdue_job"] = job
	}
	return out
}

func (s *Server) runJob(name string) bool {
	return s.jobs != nil && s.jobs.RunNow(name)
}
