package http

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/metrics"
)

// slowQueryThreshold is when a statement is logged as slow.
const slowQueryThreshold = 50 * time.Millisecond

type routeKey struct{}

// routeHolder learns the matched ServeMux pattern: the mux sets
// Request.Pattern on the request it receives, which is a copy of the one the
// outer middleware holds.
type routeHolder struct{ pattern string }

// muxRoute records the pattern the mux matched (wraps the mux directly).
func muxRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok {
			h.pattern = r.Pattern
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// hashUser shortens a user ID to a stable, non-reversible log handle.
func hashUser(id string) string {
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}

// observeMiddleware writes one structured log line per request (request ID,
// hashed user, route, status, duration) and records request metrics. API
// calls log at info, static files at debug, server errors at warn.
func (s *Server) observeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := auth.RandomToken(8)
		w.Header().Set("X-Request-ID", reqID)
		holder := &routeHolder{}
		ctx := context.WithValue(r.Context(), routeKey{}, holder)
		ctx, info := auth.WithRequestInfo(ctx)
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		d := time.Since(start)
		route := holder.pattern
		if route == "" {
			route = "unmatched"
		}
		s.metrics.Add("cato_http_requests_total", "HTTP requests by route and status.",
			metrics.Labels{"route", route, "code", itoa(sw.status)}, 1)
		s.metrics.ObserveDuration("cato_http_request_duration_seconds", "HTTP request latency by route.",
			metrics.Labels{"route", route}, d)

		level := slog.LevelDebug
		if strings.HasPrefix(r.URL.Path, "/api/") {
			level = slog.LevelInfo
		}
		if sw.status >= 500 {
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "request", "id", reqID, "method", r.Method, "route", route,
			"path", r.URL.Path, "status", sw.status, "bytes", sw.bytes,
			"ms", float64(d.Microseconds())/1000, "user", hashUser(info.UserID), "ip", auth.ClientIP(r))
	})
}

func itoa(n int) string {
	if n >= 100 && n < 1000 {
		return string([]byte{byte('0' + n/100), byte('0' + n/10%10), byte('0' + n%10)})
	}
	return "other"
}

// observeQuery is the database statement hook: metrics plus the slow log.
func (s *Server) observeQuery(query string, d time.Duration) {
	s.metrics.ObserveDuration("cato_db_statement_seconds", "Database statement latency.", nil, d)
	if d >= slowQueryThreshold {
		s.metrics.Add("cato_db_slow_statements_total", "Statements slower than 50ms.", nil, 1)
		q := strings.Join(strings.Fields(query), " ")
		if len(q) > 300 {
			q = q[:300] + "…"
		}
		slog.Warn("slow query", "ms", d.Milliseconds(), "sql", q)
	}
}

// handleMetrics serves /metrics in the Prometheus text format. With
// CATO_METRICS_TOKEN it needs that bearer token; otherwise only loopback and
// private-network clients may scrape.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.MetricsToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !auth.TokensEqual(got, s.cfg.MetricsToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	} else if ip, err := netip.ParseAddr(auth.ClientIP(r)); err != nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.metrics.Write(w)
}

// collectGauges sets the scrape-time gauges.
func (s *Server) collectGauges(r *metrics.Registry) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(s.cfg.DBPath + suffix); err == nil {
			name := "database"
			if suffix != "" {
				name = "wal"
			}
			r.Set("cato_db_file_bytes", "Database file sizes.", metrics.Labels{"file", name}, float64(fi.Size()))
		}
	}
	var users, items, pending int64
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_items`).Scan(&items)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cover_jobs WHERE attempts < 5`).Scan(&pending)
	r.Set("cato_users", "Accounts.", nil, float64(users))
	r.Set("cato_library_items", "Library items across all users.", nil, float64(items))
	r.Set("cato_cover_jobs_pending", "Cover downloads waiting.", nil, float64(pending))
	if s.jobs != nil {
		if sts, err := s.jobs.Statuses(ctx); err == nil {
			for _, st := range sts {
				l := metrics.Labels{"job", st.Name}
				r.Set("cato_job_lag_seconds", "How overdue each background job is.", l, st.LagSeconds)
				r.Set("cato_job_failures", "Failed runs per job.", l, float64(st.Failures))
				r.Set("cato_job_runs", "Runs per job.", l, float64(st.Runs))
			}
		}
	}
	for _, ln := range s.catalog.LaneStatus() {
		l := metrics.Labels{"lane", ln.Lane}
		r.Set("cato_igdb_queued", "IGDB requests waiting per lane.", l, float64(ln.Queued))
		r.Set("cato_igdb_requests", "IGDB requests sent per lane.", l, float64(ln.Requests))
	}
}
