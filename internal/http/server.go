package http

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
)

type Server struct {
	cfg        *config.Config
	db         *db.DB
	mux        *http.ServeMux
	httpServer *http.Server
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

// gzipMiddleware gzips JSON API responses when the client accepts gzip.
// Static files and covers are excluded: they are served via
// http.ServeFile/http.FileServer, which implement Range requests — wrapping
// them produced corrupt responses (206 Partial Content with
// Content-Encoding: gzip but raw uncompressed bytes). Vary is set whenever
// content negotiation happens so caches never serve an encoded variant to a
// client that can't decode it.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := strings.HasPrefix(r.URL.Path, "/api/")
		acceptsGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")

		if !isAPI || !acceptsGzip {
			if isAPI {
				w.Header().Add("Vary", "Accept-Encoding")
			}
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter defers enabling Content-Encoding until a handler
// actually writes a body, so 204/304 responses and HEAD requests are not
// tagged with an encoding their (empty) body doesn't have.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if code == http.StatusNoContent || code == http.StatusNotModified {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.gz = gzip.NewWriter(w.ResponseWriter)
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz == nil {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

func (w *gzipResponseWriter) Close() {
	if w.gz != nil {
		w.gz.Close()
	}
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
	s.httpServer = &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: s.Handler(),
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests. Previously SIGTERM just
// killed the process, dropping any request mid-flight (including DB writes).
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}
