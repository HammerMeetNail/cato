package http

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
)

type Server struct {
	cfg *config.Config
	db  *db.DB
	mux *http.ServeMux
}

func NewServer(cfg *config.Config, db *db.DB) *Server {
	s := &Server{
		cfg: cfg,
		db:  db,
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)

	authHandler := NewAuthHandler(s.db, s.cfg)
	authHandler.Register(s.mux)

	gameHandler := NewGameHandler(s.db, s.cfg)
	gameHandler.Register(s.mux)

	libraryHandler := NewLibraryHandler(s.db)
	libraryHandler.Register(s.mux)

	s.mux.HandleFunc("/covers/", covers.ServeCover(s.cfg.CoverDir))

	// Page routes
	s.mux.HandleFunc("/login", s.servePage("login.html"))
	s.mux.HandleFunc("/library", s.servePage("index.html"))
	s.mux.HandleFunc("/settings", s.servePage("settings.html"))

	// Static files with cache headers
	fs := http.FileServer(http.Dir(s.cfg.StaticDir))
	s.mux.Handle("/", staticCacheMiddleware(fs))
}

// staticCacheMiddleware sets caching policy for static assets. JS/CSS use
// "no-cache" — the browser MAY cache but MUST revalidate every load (a cheap
// 304 when unchanged). Because these files have stable names (no content hash /
// no build step), a long max-age would serve stale JS for the whole TTL after a
// deploy. Covers are NOT handled here; they get their own long immutable cache
// in covers.ServeCover (safe because they're keyed by immutable game ID).
func staticCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/js/") ||
			strings.HasPrefix(r.URL.Path, "/css/") ||
			r.URL.Path == "/favicon.svg" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// gzipMiddleware wraps the response writer to gzip the body if the client accepts gzip.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip gzip for cover images (already compressed or not worth gzipping).
		if strings.HasPrefix(r.URL.Path, "/covers/") {
			next.ServeHTTP(w, r)
			return
		}

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()

		// Wrap the response writer to gzip writes
		gzWriter := &gzipResponseWriter{ResponseWriter: w, writer: gz}
		next.ServeHTTP(gzWriter, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}

func (s *Server) servePage(filename string) http.HandlerFunc {
	path := filepath.Join(s.cfg.StaticDir, filename)
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := "ok"
	dbStatus := "ok"

	if err := s.db.Ping(); err != nil {
		status = "degraded"
		dbStatus = "unreachable"
	}

	resp := map[string]string{
		"status":   status,
		"database": dbStatus,
	}

	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) Handler() http.Handler {
	return gzipMiddleware(s.mux)
}

func (s *Server) Start() error {
	server := &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: s.Handler(),
	}
	return server.ListenAndServe()
}
