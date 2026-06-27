package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const (
	SessionKey contextKey = "session"
	UserIDKey  contextKey = "user_id"
)

func GetSessionID(r *http.Request) string {
	cookie, err := r.Cookie("cato_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func SetSessionCookie(w http.ResponseWriter, sessionID string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     "cato_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     "cato_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   -1,
	})
}

func AuthRequired(db Querier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessionID := GetSessionID(r)
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
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func CSRFRequired(db Querier) func(http.Handler) http.Handler {
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

			sessionID := GetSessionID(r)
			if sessionID == "" {
				writeJSON(w, http.StatusForbidden, errResp("csrf_missing", "CSRF token required"))
				return
			}

			session, err := GetSession(db, sessionID)
			if err != nil || session == nil {
				writeJSON(w, http.StatusForbidden, errResp("csrf_invalid", "Invalid session"))
				return
			}

			if !strings.EqualFold(session.CSRFToken, csrfToken) {
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

