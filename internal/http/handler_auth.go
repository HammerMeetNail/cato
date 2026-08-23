package http

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/db"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type AuthHandler struct {
	db              *db.DB
	cfg             *config.Config
	googleCfg       *oauth2.Config
	loginLimiter    *auth.RateLimiter
	signupLimiter   *auth.RateLimiter
	passwordLimiter *auth.RateLimiter
}

func NewAuthHandler(db *db.DB, cfg *config.Config) *AuthHandler {
	h := &AuthHandler{
		db:              db,
		cfg:             cfg,
		loginLimiter:    auth.NewRateLimiter(10, time.Minute),
		signupLimiter:   auth.NewRateLimiter(5, time.Minute),
		passwordLimiter: auth.NewRateLimiter(10, time.Minute),
	}

	if cfg.GoogleKey != "" && cfg.GoogleSecret != "" {
		// The redirect URL must be reachable by the user's browser, so it
		// must use the externally visible base URL — not the listen address
		// (":7080" would produce "http://localhost:7080/...", which breaks
		// for anyone not on the server itself, e.g. the Docker deployment).
		var redirectURL string
		if cfg.BaseURL != "" {
			redirectURL = strings.TrimSuffix(cfg.BaseURL, "/") + "/api/auth/google/callback"
		} else {
			redirectURL = "http://localhost" + cfg.ListenAddr + "/api/auth/google/callback"
			log.Printf("WARNING: GOOGLE_KEY is set but CATO_BASE_URL is not; " +
				"Google OAuth redirect will use http://localhost — set CATO_BASE_URL " +
				"(e.g. http://10.0.0.42:7080) for sign-in to work from other devices")
		}
		h.googleCfg = auth.NewGoogleConfig(cfg.GoogleKey, cfg.GoogleSecret, redirectURL)
	}

	return h
}

func (h *AuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/me", h.handleMe)

	signupChain := h.signupLimiter.Middleware(http.HandlerFunc(h.handleSignup))
	mux.Handle("/api/auth/signup", signupChain)

	loginChain := h.loginLimiter.Middleware(http.HandlerFunc(h.handleLogin))
	mux.Handle("/api/auth/login", loginChain)

	mux.HandleFunc("/api/auth/logout", h.handleLogout)

	// Change password: auth + CSRF + rate limited (the current password is
	// verified inside, so the limiter slows online guessing).
	passwordChain := h.passwordLimiter.Middleware(
		auth.AuthRequired(h.db)(auth.CSRFRequired(h.db)(http.HandlerFunc(h.handleChangePassword))))
	mux.Handle("/api/auth/password", passwordChain)

	mux.HandleFunc("/api/auth/google/start", h.handleGoogleStart)
	mux.HandleFunc("/api/auth/google/callback", h.handleGoogleCallback)
}

func (h *AuthHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		h.updateMe(w, r)
		return
	}

	if r.Method == http.MethodDelete {
		h.deleteMe(w, r)
		return
	}

	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	sessionID := auth.GetSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	session, err := auth.GetSession(h.db, sessionID)
	if err != nil || session == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	var email, displayName string
	var hasPassword bool
	err = h.db.QueryRow("SELECT email, display_name, COALESCE(password_hash, '') != '' FROM users WHERE id = ?", session.UserID).Scan(&email, &displayName, &hasPassword)
	if err == sql.ErrNoRows {
		// Session points at a deleted user — treat as unauthenticated.
		auth.DeleteSession(h.db, sessionID)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to load user"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"user_id":       session.UserID,
		"email":         email,
		"display_name":  displayName,
		"has_password":  hasPassword,
		"csrf_token":    session.CSRFToken,
	})
}

// updateMe handles PATCH /api/me — currently just the display name. The
// route deliberately has no middleware (GET must answer {authenticated:
// false} for the login page), so session + CSRF are validated inline with
// the same rules as auth.CSRFRequired.
func (h *AuthHandler) updateMe(w http.ResponseWriter, r *http.Request) {
	sessionID := auth.GetSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Authentication required"))
		return
	}
	session, err := auth.GetSession(h.db, sessionID)
	if err != nil || session == nil {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
		return
	}
	if !strings.EqualFold(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		writeJSON(w, http.StatusForbidden, errResp("csrf_mismatch", "CSRF token mismatch"))
		return
	}

	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if len(req.DisplayName) > 64 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_display_name", "Display name must be at most 64 characters"))
		return
	}

	if _, err := h.db.Exec(`UPDATE users SET display_name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		req.DisplayName, session.UserID); err != nil {
		log.Printf("update display name for %s: %v", session.UserID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to update profile"))
		return
	}

	var email string
	if err := h.db.QueryRow("SELECT email FROM users WHERE id = ?", session.UserID).Scan(&email); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to load user"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"user_id":       session.UserID,
		"email":         email,
		"display_name":  req.DisplayName,
	})
}

// handleChangePassword handles POST /api/auth/password. The current password
// is verified before the new one is stored, so possession of the session
// cookie alone is not enough to take over the account.
func (h *AuthHandler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_request", "Current and new passwords are required"))
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, errResp("weak_password", "Password must be at least 8 characters"))
		return
	}
	if len(req.NewPassword) > 72 {
		// bcrypt only considers the first 72 bytes; reject rather than
		// silently truncating (same limit as nabu).
		writeJSON(w, http.StatusBadRequest, errResp("weak_password", "Password must be at most 72 characters"))
		return
	}

	var passwordHash string
	err := h.db.QueryRow("SELECT COALESCE(password_hash, '') FROM users WHERE id = ?", userID).Scan(&passwordHash)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
		return
	}
	if err != nil {
		log.Printf("load password hash for %s: %v", userID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to change password"))
		return
	}
	if passwordHash == "" {
		// Google-linked account created without a local password.
		writeJSON(w, http.StatusBadRequest, errResp("no_password_set", "This account signs in with Google and has no password to change"))
		return
	}
	if !auth.CheckPassword(req.CurrentPassword, passwordHash) {
		writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Current password is incorrect"))
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to process password"))
		return
	}

	if _, err := h.db.Exec(`UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		newHash, userID); err != nil {
		log.Printf("change password for %s: %v", userID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to change password"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"message":       "Password updated",
	})
}

// deleteMe handles DELETE /api/me — permanent account deletion. The body must
// carry the typed confirmation {"confirm":"DELETE"} so a single stray or
// forged request cannot destroy the account (parity with nabu). sessions and
// library_items reference users(id) ON DELETE CASCADE, so removing the user
// row cleans up everything owned by the account.
func (h *AuthHandler) deleteMe(w http.ResponseWriter, r *http.Request) {
	sessionID := auth.GetSessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Authentication required"))
		return
	}
	session, err := auth.GetSession(h.db, sessionID)
	if err != nil || session == nil {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
		return
	}
	if !strings.EqualFold(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		writeJSON(w, http.StatusForbidden, errResp("csrf_mismatch", "CSRF token mismatch"))
		return
	}

	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Confirm != "DELETE" {
		writeJSON(w, http.StatusBadRequest, errResp("confirm_required", `Type DELETE to confirm: send {"confirm":"DELETE"}`))
		return
	}

	res, err := h.db.Exec("DELETE FROM users WHERE id = ?", session.UserID)
	if err != nil {
		log.Printf("delete account for %s: %v", session.UserID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to delete account"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Account not found"))
		return
	}

	auth.ClearSessionCookie(w, h.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Account deleted",
	})
}

func (h *AuthHandler) handleSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_email", "Valid email is required"))
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, errResp("weak_password", "Password must be at least 8 characters"))
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to process password"))
		return
	}

	userID := uuid.New().String()
	_, err = h.db.Exec(
		"INSERT INTO users (id, email, password_hash) VALUES (?, ?, ?)",
		userID, req.Email, passwordHash,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, errResp("email_taken", "A user with that email already exists"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create user"))
		return
	}

	session, err := auth.CreateSession(h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create session"))
		return
	}

	auth.SetSessionCookie(w, session.ID, h.cfg.CookieSecure)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user_id":      userID,
		"email":        req.Email,
		"authenticated": true,
		"csrf_token":   session.CSRFToken,
	})
}

func (h *AuthHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_credentials", "Email and password are required"))
		return
	}

	var userID, passwordHash, displayName string
	var disabled int
	err := h.db.QueryRow(
		"SELECT id, password_hash, COALESCE(display_name, ''), disabled FROM users WHERE email = ?",
		req.Email,
	).Scan(&userID, &passwordHash, &displayName, &disabled)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Invalid email or password"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Login failed"))
		return
	}

	if disabled != 0 {
		writeJSON(w, http.StatusForbidden, errResp("account_disabled", "Account is disabled"))
		return
	}

	if passwordHash == "" || !auth.CheckPassword(req.Password, passwordHash) {
		writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Invalid email or password"))
		return
	}

	session, err := auth.CreateSession(h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create session"))
		return
	}

	auth.SetSessionCookie(w, session.ID, h.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":       userID,
		"email":         req.Email,
		"display_name":  displayName,
		"authenticated": true,
		"csrf_token":    session.CSRFToken,
	})
}

func (h *AuthHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	sessionID := auth.GetSessionID(r)
	if sessionID != "" {
		auth.DeleteSession(h.db, sessionID)
	}

	auth.ClearSessionCookie(w, h.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Logged out",
	})
}

func (h *AuthHandler) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if h.googleCfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, errResp("google_unavailable", "Google auth is not configured"))
		return
	}

	state := auth.RandomToken(16)

	// Store state in a dedicated short-lived cookie for CSRF protection.
	// (This must NOT touch the cato_session cookie — overwriting it here
	// used to log out any already signed-in user who clicked the Google
	// button.)
	http.SetCookie(w, &http.Cookie{
		Name:     "cato_oauth_state",
		Value:    state,
		Path:     "/api/auth/google",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.CookieSecure,
		MaxAge:   600,
	})

	url := h.googleCfg.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *AuthHandler) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if h.googleCfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, errResp("google_unavailable", "Google auth is not configured"))
		return
	}

	state := r.URL.Query().Get("state")
	stateCookie, err := r.Cookie("cato_oauth_state")
	if err != nil || state == "" || stateCookie.Value != state {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_state", "Invalid OAuth state"))
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "cato_oauth_state",
		Value:    "",
		Path:     "/api/auth/google",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.CookieSecure,
		MaxAge:   -1,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing_code", "Authorization code missing"))
		return
	}

	googleUser, err := auth.FetchGoogleUser(r.Context(), h.googleCfg, code)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("google_error", "Failed to verify Google account"))
		return
	}

	userID, err := h.findOrCreateGoogleUser(googleUser)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to process account"))
		return
	}

	session, err := auth.CreateSession(h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create session"))
		return
	}

	auth.SetSessionCookie(w, session.ID, h.cfg.CookieSecure)
	http.Redirect(w, r, "/library", http.StatusFound)
}

func (h *AuthHandler) findOrCreateGoogleUser(gu *auth.GoogleUser) (string, error) {
	var userID string
	err := h.db.QueryRow(
		"SELECT id FROM users WHERE google_subject = ?",
		gu.Sub,
	).Scan(&userID)
	if err == nil {
		return userID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	// Try by email
	err = h.db.QueryRow(
		"SELECT id FROM users WHERE email = ?",
		gu.Email,
	).Scan(&userID)
	if err == nil {
		// Link Google account to existing user
		h.db.Exec("UPDATE users SET google_subject = ?, avatar_url = ? WHERE id = ?", gu.Sub, gu.Picture, userID)
		return userID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	if gu.Email == "" {
		return "", fmt.Errorf("google user has no email")
	}
	// Create new user
	userID = uuid.New().String()
	_, err = h.db.Exec(
		"INSERT INTO users (id, email, display_name, avatar_url, google_subject) VALUES (?, ?, ?, ?, ?)",
		userID, gu.Email, gu.Name, gu.Picture, gu.Sub,
	)
	if err != nil {
		return "", err
	}
	return userID, nil
}
