package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

type contextKey string

const (
	SessionKey contextKey = "session"
	UserIDKey  contextKey = "user_id"
)

func AuthRequired(db Querier, cookies Cookies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessionID := cookies.SessionID(r)
			if sessionID == "" {
				writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Authentication required"))
				return
			}

			session, err := GetSession(db, sessionID)
			if err != nil || session == nil {
				writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
				return
			}

			ctx := context.WithValue(r.Context(), SessionKey, session)
			ctx = context.WithValue(ctx, UserIDKey, session.UserID)
			if info := RequestInfoFrom(ctx); info != nil {
				info.UserID = session.UserID
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func CSRFRequired(db Querier, cookies Cookies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			csrfToken := r.Header.Get("X-CSRF-Token")
			if csrfToken == "" {
				writeJSON(w, http.StatusForbidden, errResp("csrf_missing", "CSRF token required"))
				return
			}

			// AuthRequired already loaded the session: reuse it instead of a
			// second lookup per mutation.
			session, _ := r.Context().Value(SessionKey).(*Session)
			if session == nil {
				sessionID := cookies.SessionID(r)
				if sessionID == "" {
					writeJSON(w, http.StatusForbidden, errResp("csrf_missing", "CSRF token required"))
					return
				}
				var err error
				session, err = GetSession(db, sessionID)
				if err != nil || session == nil {
					writeJSON(w, http.StatusForbidden, errResp("csrf_invalid", "Invalid session"))
					return
				}
			}

			if !TokensEqual(session.CSRFToken, csrfToken) {
				writeJSON(w, http.StatusForbidden, errResp("csrf_mismatch", "CSRF token mismatch"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func GetUserID(ctx context.Context) string {
	v, _ := ctx.Value(UserIDKey).(string)
	return v
}

func GetCSRFToken(ctx context.Context) string {
	v, _ := ctx.Value(SessionKey).(*Session)
	if v == nil {
		return ""
	}
	return v.CSRFToken
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errResp(code, message string) map[string]string {
	return map[string]string{"error": code, "message": message}
}

// RequestInfo collects per-request facts for the access log; handlers deep
// in the chain fill it in (the log middleware owns it).
type RequestInfo struct {
	UserID string
}

type requestInfoKey struct{}

// WithRequestInfo attaches a fresh RequestInfo to ctx.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	info := &RequestInfo{}
	return context.WithValue(ctx, requestInfoKey{}, info), info
}

// RequestInfoFrom returns ctx's RequestInfo, or nil.
func RequestInfoFrom(ctx context.Context) *RequestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*RequestInfo)
	return info
}
