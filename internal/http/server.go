package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
)

type Server struct {
	cfg         *config.Config
	db          *db.DB
	mux         *http.ServeMux
	httpServer  *http.Server
	gameHandler *GameHandler
}

func NewServer(cfg *config.Config, db *db.DB) *Server {
	s := &Server{
		cfg: cfg,
		db:  db,
		mux: http.NewServeMux(),
	}
	s.routes()
	s.httpServer = &http.Server{Addr: cfg.ListenAddr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)

	authHandler := NewAuthHandler(s.db, s.cfg)
	authHandler.Register(s.mux)

	gameHandler := NewGameHandler(s.db, s.cfg)
	gameHandler.Register(s.mux)
	s.gameHandler = gameHandler

	libraryHandler := NewLibraryHandler(s.db)
	libraryHandler.Register(s.mux)

	s.mux.HandleFunc("/covers/", covers.ServeCover(s.cfg.CoverDir))

	// Page routes
	s.mux.HandleFunc("/login", s.servePage("login.html"))
	s.mux.HandleFunc("/library", s.servePage("index.html"))
	// Settings is now a SPA tab (#settings). Keep /settings as a redirect so
	// old links/bookmarks land in the right place without a full-page reload loop.
	s.mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/#settings", http.StatusFound)
	})

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
//
// HTML is also no-cache: without an explicit header, browsers heuristically
// cache pages based on Last-Modified, so phones kept serving the pre-deploy
// markup (with fresh CSS/JS) until the heuristic expired.
//
// PWA assets (manifest, service-worker, offline.html, icons) are also no-cache
// so installs and updates are not stuck on stale versions. Icons could be long-
// cached, but they are tiny and change rarely; no-cache keeps the logic simple.
func staticCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/js/") ||
			strings.HasPrefix(r.URL.Path, "/css/") ||
			strings.HasPrefix(r.URL.Path, "/icons/") ||
			r.URL.Path == "/favicon.svg" ||
			r.URL.Path == "/manifest.webmanifest" ||
			r.URL.Path == "/service-worker.js" ||
			r.URL.Path == "/offline.html" ||
			r.URL.Path == "/" ||
			strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// Service worker must never be cached by the browser HTTP cache — it is
		// already Cache API cached by the SW itself and update checks rely on
		// a fresh network fetch.
		if r.URL.Path == "/service-worker.js" {
			w.Header().Set("Cache-Control", "no-store")
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
		// The named page routes bypass staticCacheMiddleware; keep HTML
		// revalidated so deploys reach clients immediately.
		w.Header().Set("Cache-Control", "no-cache")
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

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	readErr := s.db.Read.PingContext(ctx)
	writeErr := s.db.Write.PingContext(ctx)
	if readErr != nil || writeErr != nil {
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
	return securityMiddleware(gzipMiddleware(apiBodyLimitMiddleware(s.mux)))
}

func (s *Server) Start() error {
	s.gameHandler.startBackground(s.cfg)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests. Previously SIGTERM just
// killed the process, dropping any request mid-flight (including DB writes).
func (s *Server) Shutdown(ctx context.Context) error {
	drainErr := s.httpServer.Shutdown(ctx)
	if drainErr != nil {
		_ = s.httpServer.Close()
	}
	workerErr := s.gameHandler.service.Shutdown(ctx)
	return errors.Join(drainErr, workerErr)
}

const maxAPIBodyBytes = 1 << 20

// Read before dispatch so even chunked bodies and trailing bytes are checked
// before a handler can mutate state or commit a different error response.
func apiBodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Body != nil {
			body := http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
			data, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				var oversized *http.MaxBytesError
				if errors.As(err, &oversized) {
					writeJSON(w, http.StatusRequestEntityTooLarge, errResp("request_too_large", "Request body exceeds 1 MiB"))
				} else {
					writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Could not read request body"))
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		next.ServeHTTP(w, r)
	})
}

func securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
