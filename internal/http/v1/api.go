// Package v1 serves /api/v1: thin handlers over internal/library and
// internal/catalog. Conventions (docs/api-v1.md):
//
//   - snake_case JSON; collections are {items, next_cursor, total}, with
//     total only on the first page;
//   - errors are {error, message, fields} with fields naming invalid inputs;
//   - mutations return the changed resource plus library_rev and an undo_id;
//   - every GET carries an ETag and answers If-None-Match with 304.
package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/discover"
	"cato/internal/imports"
	"cato/internal/library"
	"cato/internal/notify"
	"cato/internal/social"
	"cato/internal/webauthn"
)

// Deps are what the API needs from the server.
type Deps struct {
	DB      *db.DB
	Cfg     *config.Config
	Cookies auth.Cookies
	Library *library.Store
	Catalog *catalog.Service
	Games   *catalog.Store
	// SearchLimiter bounds catalog search per client IP.
	SearchLimiter *auth.RateLimiter
	Discover      *discover.Service
	CoverDir      string
	Social        *social.Service
	Imports       *imports.Service
	Notify        *notify.Service
	Barcode       *imports.Barcode
	Passkeys      *webauthn.Challenges
	// System reports server health for the admin System page; RunJob
	// starts a background job now.
	System func(context.Context) map[string]any
	RunJob func(name string) bool
}

type API struct {
	Deps
	// writes limits mutations per user.
	writes *auth.RateLimiter
}

// writesPerMinute bounds one account's mutations: generous for people
// (bulk edits are one request), a brake on runaway scripts.
const writesPerMinute = 300

// Register mounts every /api/v1 route on mux.
func Register(mux *http.ServeMux, d Deps) *API {
	a := &API{Deps: d, writes: auth.NewRateLimiter(writesPerMinute, time.Minute)}
	authed := func(h http.HandlerFunc) http.Handler {
		return auth.AuthRequired(d.DB, d.Cookies)(auth.CSRFRequired(d.DB, d.Cookies)(a.limitWrites(etag(h))))
	}
	route := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, authed(h)) }

	route("GET /api/v1/me", a.getMe)
	route("PATCH /api/v1/me/prefs", a.patchPrefs)

	route("GET /api/v1/library", a.listLibrary)
	route("GET /api/v1/library/index", a.libraryIndex)
	route("GET /api/v1/library/counts", a.libraryCounts)
	route("GET /api/v1/library/tags", a.libraryTags)
	route("GET /api/v1/library/platforms", a.libraryPlatforms)
	route("POST /api/v1/library/bulk", a.bulk)
	route("GET /api/v1/library/{id}", a.getItem)
	route("PUT /api/v1/library/{id}", a.putItem)
	route("PATCH /api/v1/library/{id}", a.patchItem)
	route("DELETE /api/v1/library/{id}", a.deleteItem)
	route("GET /api/v1/library/{id}/playthroughs", a.listPlaythroughs)
	route("POST /api/v1/undo/{id}", a.undo)

	route("GET /api/v1/sessions", a.listSessions)
	route("POST /api/v1/sessions", a.createSession)
	route("PATCH /api/v1/sessions/{id}", a.patchSession)
	route("DELETE /api/v1/sessions/{id}", a.deleteSession)
	route("PATCH /api/v1/playthroughs/{id}", a.patchPlaythrough)
	route("DELETE /api/v1/playthroughs/{id}", a.deletePlaythrough)

	route("GET /api/v1/timer", a.getTimer)
	route("POST /api/v1/timer", a.startTimer)
	route("POST /api/v1/timer/stop", a.stopTimer)

	route("GET /api/v1/stats", a.stats)
	route("GET /api/v1/home", a.home)
	route("GET /api/v1/export.json", a.exportJSON)
	route("GET /api/v1/export.csv", a.exportCSV)

	route("GET /api/v1/devices", a.devices)
	route("DELETE /api/v1/devices/{id}", a.revokeDevice)
	route("POST /api/v1/devices/revoke-others", a.revokeOtherDevices)
	route("GET /api/v1/discover", a.discover)
	route("GET /api/v1/up-next", a.upNext)
	route("GET /api/v1/genres", a.genres)
	route("POST /api/v1/games", a.createCustomGame)
	route("PUT /api/v1/games/{id}/cover", a.uploadCustomCover)

	route("GET /api/v1/games/{id}", a.getGame)
	route("GET /api/v1/platforms", a.platforms)
	route("GET /api/v1/typeahead", a.typeahead)
	route("GET /api/v1/search", a.search)
	route("POST /api/v1/diagnostics/reload", a.reportReload)
	route("POST /api/v1/diagnostics/swipe", a.reportSwipe)

	a.registerSocial(route)
	a.registerImports(route)
	a.registerReach(route)
	a.registerDelight(route)

	// Anything else under /api/v1 is a JSON 404, not the SPA shell.
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint", nil)
	})
	return a
}

// --- responses ---------------------------------------------------------------

type apiError struct {
	Error   string            `json:"error"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, apiError{Error: code, Message: msg, Fields: fields})
}

// fail maps domain errors to responses; anything unexpected is a logged 500.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *library.ValidationError
	switch {
	case errors.As(err, &ve):
		var fields map[string]string
		if ve.Field != "" {
			fields = map[string]string{ve.Field: ve.Message}
		}
		writeError(w, http.StatusBadRequest, ve.Code, ve.Message, fields)
	case errors.Is(err, library.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not in your library", nil)
	case errors.Is(err, library.ErrGameNotFound):
		writeError(w, http.StatusNotFound, "game_not_found", "Game not found", nil)
	case errors.Is(err, library.ErrBadCursor):
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Invalid pagination cursor", map[string]string{"cursor": "invalid"})
	case errors.Is(err, library.ErrUndoExpired):
		writeError(w, http.StatusGone, "undo_expired", "That can no longer be undone", nil)
	case errors.Is(err, library.ErrNoTimer):
		writeError(w, http.StatusConflict, "no_timer", "No session timer is running", nil)
	case errors.Is(err, context.Canceled):
		// The client went away.
	default:
		slog.ErrorContext(r.Context(), "api error", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Something went wrong", nil)
	}
}

// decode reads a JSON body into v (the body is size-limited upstream).
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "Expected a JSON body", nil)
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON: "+err.Error(), nil)
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid ID", map[string]string{"id": "must be a number"})
		return 0, false
	}
	return id, true
}

func user(r *http.Request) string { return auth.GetUserID(r.Context()) }

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

// --- middleware --------------------------------------------------------------

func (a *API) limitWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.writes.Allow(user(r)) {
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many changes at once. Try again shortly.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// etag gives every successful GET a weak ETag of its body and answers a
// matching If-None-Match with 304. Library handlers short-circuit earlier
// with notModified (library_rev) to skip the query too.
func etag(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next(w, r)
			return
		}
		bw := &bufferedWriter{header: http.Header{}, status: http.StatusOK}
		next(bw, r)
		for k, v := range bw.header {
			w.Header()[k] = v
		}
		if bw.status == http.StatusNotModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if bw.status == http.StatusOK && w.Header().Get("ETag") == "" && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
			sum := sha256.Sum256(bw.body)
			tag := `W/"` + hex.EncodeToString(sum[:12]) + `"`
			w.Header().Set("ETag", tag)
			if matches(r, tag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(bw.status)
		w.Write(bw.body)
	}
}

func matches(r *http.Request, tag string) bool {
	for _, c := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		c = strings.TrimSpace(c)
		if c == "*" || strings.TrimPrefix(c, "W/") == strings.TrimPrefix(tag, "W/") {
			return true
		}
	}
	return false
}

// notModified sets the ETag for a library response computed from the
// user's library_rev and the request's query, and reports whether the
// client already has it (one indexed lookup instead of the full query).
func (a *API) notModified(w http.ResponseWriter, r *http.Request, scope string) bool {
	rev, err := a.Library.Rev(r.Context(), user(r))
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(scope + "\x00" + user(r) + "\x00" + r.URL.RawQuery))
	tag := `W/"lib-` + strconv.FormatInt(rev, 10) + "-" + hex.EncodeToString(sum[:6]) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("X-Library-Rev", strconv.FormatInt(rev, 10))
	if matches(r, tag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

type bufferedWriter struct {
	header http.Header
	status int
	body   []byte
	wrote  bool
}

func (b *bufferedWriter) Header() http.Header { return b.header }
func (b *bufferedWriter) WriteHeader(code int) {
	if !b.wrote {
		b.status, b.wrote = code, true
	}
}
func (b *bufferedWriter) Write(p []byte) (int, error) {
	b.wrote = true
	b.body = append(b.body, p...)
	return len(p), nil
}
