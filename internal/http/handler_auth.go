package http

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/config"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type AuthHandler struct {
	db            *sql.DB
	cfg           *config.Config
	googleCfg     *oauth2.Config
	loginLimiter  *auth.RateLimiter
	signupLimiter *auth.RateLimiter
}

func NewAuthHandler(db *sql.DB, cfg *config.Config) *AuthHandler {
	h := &AuthHandler{
		db:            db,
		cfg:           cfg,
		loginLimiter:  auth.NewRateLimiter(10, time.Minute),
		signupLimiter: auth.NewRateLimiter(5, time.Minute),
	}

	if cfg.GoogleKey != "" && cfg.GoogleSecret != "" {
		redirectURL := "http://" + cfg.ListenAddr + "/api/auth/google/callback"
		if strings.HasPrefix(cfg.ListenAddr, ":") {
			redirectURL = "http://localhost" + cfg.ListenAddr + "/api/auth/google/callback"
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
	mux.HandleFunc("/api/auth/google/start", h.handleGoogleStart)
	mux.HandleFunc("/api/auth/google/callback", h.handleGoogleCallback)
}

func (h *AuthHandler) handleMe(w http.ResponseWriter, r *http.Request) {
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
	h.db.QueryRow("SELECT email, display_name FROM users WHERE id = ?", session.UserID).Scan(&email, &displayName)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"user_id":       session.UserID,
		"email":         email,
		"display_name":  displayName,
		"csrf_token":    session.CSRFToken,
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

	var userID, passwordHash string
	var disabled int
	err := h.db.QueryRow(
		"SELECT id, password_hash, disabled FROM users WHERE email = ?",
		req.Email,
	).Scan(&userID, &passwordHash, &disabled)
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
	auth.SetSessionCookie(w, state, h.cfg.CookieSecure)

	// Store state temporarily in a cookie for CSRF protection
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
