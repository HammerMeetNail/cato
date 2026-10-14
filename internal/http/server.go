package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
	v1 "cato/internal/http/v1"
	"cato/internal/jobs"
	"cato/internal/library"
	"cato/internal/metrics"
)

type Server struct {
	cfg            *config.Config
	db             *db.DB
	mux            *http.ServeMux
	httpServer     *http.Server
	redirectServer *http.Server
	catalog        *catalogWiring
	library        *library.Store
	static         *gzipStatic
	assets         *assetIndex
	jobs           *jobs.Runner
	metrics        *metrics.Registry

	// certReloadInterval bounds how often the TLS certificate files are
	// re-checked for renewal.
	certReloadInterval time.Duration

	mu         sync.Mutex
	ln         net.Listener
	redirectLn net.Listener
}

func NewServer(cfg *config.Config, db *db.DB) *Server {
	s := &Server{
		cfg:                cfg,
		db:                 db,
		mux:                http.NewServeMux(),
		certReloadInterval: time.Minute,
		metrics:            metrics.NewRegistry(),
	}
	db.SetObserver(s.observeQuery)
	s.metrics.Collect(s.collectGauges)
	s.routes()
	s.httpServer = &http.Server{Addr: cfg.ListenAddr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if cfg.HTTPRedirectAddr != "" {
		s.redirectServer = &http.Server{Addr: cfg.HTTPRedirectAddr, Handler: s.redirectHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	}
	return s
}

func (s *Server) routes() {
	s.assets = newAssetIndex(s.cfg.StaticDir)
	s.static = newGzipStatic(s.cfg.StaticDir, http.FileServer(noListingFS{http.Dir(s.cfg.StaticDir)}))
	s.static.transform = func(urlPath string, raw []byte) []byte {
		switch {
		case strings.HasSuffix(urlPath, ".html"):
			return s.assets.rewriteHTML(raw)
		case urlPath == "/service-worker.js":
			return s.assets.rewriteServiceWorker(raw)
		}
		return raw
	}
	s.static.variant = func(urlPath string) string {
		if strings.HasSuffix(urlPath, ".html") || urlPath == "/service-worker.js" {
			return s.assets.Version()
		}
		return ""
	}
	s.mux.HandleFunc("GET /asset-manifest.json", s.assets.handleManifest)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)

	authHandler := NewAuthHandler(s.db, s.cfg)
	authHandler.Register(s.mux)

	s.catalog = newCatalogWiring(s.db, s.cfg)
	s.library = library.NewStore(s.db, s.catalog.store)

	v1.Register(s.mux, v1.Deps{
		DB:            s.db,
		Cfg:           s.cfg,
		Cookies:       auth.Cookies{Secure: s.cfg.SecureCookies()},
		Library:       s.library,
		Catalog:       s.catalog.service,
		Games:         s.catalog.store,
		SearchLimiter: s.catalog.searchLimiter,
	})

	s.mux.HandleFunc("/covers/", covers.ServeCover(s.cfg.CoverDir))

	// Page routes
	s.mux.HandleFunc("/login", s.servePage("login.html"))
	s.mux.HandleFunc("/library", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/#library", http.StatusFound)
	})
	// Settings is now a SPA tab (#settings). Keep /settings as a redirect so
	// old links/bookmarks land in the right place without a full-page reload loop.
	s.mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/#you/settings", http.StatusFound)
	})

	// Static files with cache headers; text assets are served gzipped.
	s.mux.Handle("/", s.staticCacheMiddleware(s.static))
}

// noListingFS hides directories that have no index.html, so http.FileServer
// answers 404 instead of generating a listing of the static tree.
type noListingFS struct{ fs http.FileSystem }

func (n noListingFS) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		index, err := n.fs.Open(path.Join(name, "index.html"))
		if err != nil {
			f.Close()
			return nil, fs.ErrNotExist
		}
		index.Close()
	}
	return f, nil
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
func (s *Server) staticCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A URL carrying the file's current content hash never changes.
		if s.assets.versioned(r) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			next.ServeHTTP(w, r)
			return
		}
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
// Static text assets are compressed separately (gzipStatic: precompressed,
// Range-safe); covers are already-compressed images. Wrapping Range-capable
// file responses here produced corrupt 206s (gzip-labelled raw bytes). Vary is
// set whenever content negotiation happens so caches never serve an encoded
// variant to a client that can't decode it.
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
	return func(w http.ResponseWriter, r *http.Request) {
		// The named page routes bypass staticCacheMiddleware; keep HTML
		// revalidated so deploys reach clients immediately.
		w.Header().Set("Cache-Control", "no-cache")
		s.static.serve(w, r, "/"+filename)
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

	resp := map[string]any{
		"status":   status,
		"database": dbStatus,
	}
	// Job lag is reported, not fatal: a stuck job shouldn't get the
	// container restarted, but the operator should see it.
	if s.jobs != nil {
		lag, job := s.jobs.MaxLag(ctx)
		resp["job_lag_seconds"] = int64(lag.Seconds())
		if job != "" {
			resp["most_overdue_job"] = job
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) Handler() http.Handler {
	return s.clientMiddleware(s.observeMiddleware(securityMiddleware(s.cfg, s.assets.ScriptHash, gzipMiddleware(apiBodyLimitMiddleware(muxRoute(s.mux))))))
}

// clientMiddleware resolves the client IP and scheme once per request,
// believing forwarded headers only from CATO_TRUSTED_PROXIES.
func (s *Server) clientMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, auth.ResolveClient(r, s.cfg.TrustedProxies))
	})
}

// Jobs returns the server-owned recurring tasks (catalog refresh and
// repair) for the shared job runner.
func (s *Server) Jobs() []jobs.Job { return s.catalog.Jobs(s.cfg) }

// SetJobRunner lets /healthz and the admin pages report job health.
func (s *Server) SetJobRunner(r *jobs.Runner) { s.jobs = r }

// SetCoverNotifier registers a callback (the cover worker's Wake) run after
// library writes that may have queued a cover job.
func (s *Server) SetCoverNotifier(fn func()) { s.library.OnWrite = fn }

// Listen binds the application listener (TLS when configured) and the
// optional HTTP→HTTPS redirect listener. Start calls it when needed;
// calling it first lets callers learn ":0" addresses and fail fast on a bad
// certificate before anything is served.
func (s *Server) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	var tlsCfg *tls.Config
	if s.cfg.ServesTLS() {
		reloader, err := newCertReloader(s.cfg.TLSCertFile, s.cfg.TLSKeyFile, s.certReloadInterval)
		if err != nil {
			return err
		}
		tlsCfg = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: reloader.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
		}
		s.httpServer.TLSConfig = tlsCfg
	}
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	if s.redirectServer != nil {
		rln, err := net.Listen("tcp", s.cfg.HTTPRedirectAddr)
		if err != nil {
			ln.Close()
			return err
		}
		s.redirectLn = rln
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	s.ln = ln
	return nil
}

// Addr is the bound application address (nil before Listen).
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// RedirectAddr is the bound HTTP redirect address (nil when disabled).
func (s *Server) RedirectAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redirectLn == nil {
		return nil
	}
	return s.redirectLn.Addr()
}

func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.mu.Lock()
	ln, rln := s.ln, s.redirectLn
	s.mu.Unlock()
	if rln != nil {
		go func() {
			if err := s.redirectServer.Serve(rln); err != nil && err != http.ErrServerClosed {
				log.Printf("http redirect listener: %v", err)
			}
		}()
	}
	// The listener is already TLS-wrapped, so Serve (not ServeTLS) is right;
	// HTTP/2 is negotiated through NextProtos.
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully drains in-flight requests. Previously SIGTERM just
// killed the process, dropping any request mid-flight (including DB writes).
func (s *Server) Shutdown(ctx context.Context) error {
	var redirectErr error
	if s.redirectServer != nil {
		redirectErr = s.redirectServer.Shutdown(ctx)
	}
	drainErr := s.httpServer.Shutdown(ctx)
	if drainErr != nil {
		_ = s.httpServer.Close()
	}
	workerErr := s.catalog.service.Shutdown(ctx)
	return errors.Join(drainErr, redirectErr, workerErr)
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

// permissionsPolicy turns off powerful features Cato does not use; camera
// stays available to Cato itself for barcode scanning.
const permissionsPolicy = "accelerometer=(), camera=(self), display-capture=(), geolocation=(), " +
	"gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), usb=()"

// securityMiddleware sets the security headers. scriptHash is the CSP
// source of the injected import map (the only inline script).
func securityMiddleware(cfg *config.Config, scriptHash func() string, next http.Handler) http.Handler {
	cspTail := "; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' https://images.igdb.com data: blob:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"
	https := cfg.HTTPS()
	if https {
		cspTail += "; upgrade-insecure-requests"
	}
	// HSTS is per host, not per port: on a self-signed dev certificate it
	// would pin every other localhost project to https, so dev TLS skips it.
	hsts := https && !cfg.DevTLS
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// CSP: scripts are same-origin only (no inline handlers or eval — the
		// theme/login bootstrap scripts are external files for this reason).
		// style-src allows 'unsafe-inline' because cards set inline style
		// attributes (--chip-color); style injection is a cosmetic risk, not
		// a script-execution one. img-src covers same-origin covers plus the
		// IGDB image CDN; covers from dead legacy hosts fail to load and the
		// frontend's delegated error fallback swaps in the local placeholder.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' "+scriptHash()+cspTail)
		w.Header().Set("Permissions-Policy", permissionsPolicy)
		if hsts {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
