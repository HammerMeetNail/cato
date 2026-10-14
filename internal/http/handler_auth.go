package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
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
	cookies         auth.Cookies
	googleCfg       *oauth2.Config
	loginLimiter    *auth.RateLimiter
	signupLimiter   *auth.RateLimiter
	passwordLimiter *auth.RateLimiter
	loginThrottle   *auth.LoginThrottle
	// checkPassword is auth.CheckPassword; tests count calls through it.
	checkPassword func(password, hash string) bool
}

func NewAuthHandler(db *db.DB, cfg *config.Config) *AuthHandler {
	// CATO_AUTH_RATE_LIMIT (cfg.AuthRateLimit) overrides the per-IP buckets
	// when > 0 so test harnesses with many parallel clients sharing one IP
	// aren't rate-limited. Production leaves it unset and gets the defaults.
	loginLimit, signupLimit, passwordLimit := 10, 5, 10
	if cfg != nil && cfg.AuthRateLimit > 0 {
		loginLimit, signupLimit, passwordLimit = cfg.AuthRateLimit, cfg.AuthRateLimit, cfg.AuthRateLimit
	}
	h := &AuthHandler{
		db:              db,
		cfg:             cfg,
		cookies:         auth.Cookies{Secure: cfg.SecureCookies()},
		loginLimiter:    auth.NewRateLimiter(loginLimit, time.Minute),
		signupLimiter:   auth.NewRateLimiter(signupLimit, time.Minute),
		passwordLimiter: auth.NewRateLimiter(passwordLimit, time.Minute),
		loginThrottle:   auth.NewLoginThrottle(),
		checkPassword:   auth.CheckPassword,
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
			scheme := "http"
			if cfg.HTTPS() {
				scheme = "https"
			}
			redirectURL = scheme + "://localhost" + cfg.ListenAddr + "/api/auth/google/callback"
			log.Printf("WARNING: GOOGLE_KEY is set but CATO_BASE_URL is not; "+
				"Google OAuth redirect will use %s://localhost — set CATO_BASE_URL "+
				"(e.g. https://cato.example) for sign-in to work from other devices", scheme)
		}
		h.googleCfg = auth.NewGoogleConfig(cfg.GoogleKey, cfg.GoogleSecret, redirectURL)
	}

	return h
}

func (h *AuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/me", h.handleMe)
	mux.HandleFunc("/api/config", h.handleConfig)

	signupChain := h.signupLimiter.Middleware(http.HandlerFunc(h.handleSignup))
	mux.Handle("/api/auth/signup", signupChain)

	loginChain := h.loginLimiter.Middleware(http.HandlerFunc(h.handleLogin))
	mux.Handle("/api/auth/login", loginChain)

	mux.HandleFunc("/api/auth/logout", h.handleLogout)

	// Change password: auth + CSRF + rate limited (the current password is
	// verified inside, so the limiter slows online guessing).
	passwordChain := h.passwordLimiter.Middleware(
		auth.AuthRequired(h.db, h.cookies)(auth.CSRFRequired(h.db, h.cookies)(http.HandlerFunc(h.handleChangePassword))))
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

	sessionID := h.cookies.SessionID(r)
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
	sessionID := h.cookies.SessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Authentication required"))
		return
	}
	session, err := auth.GetSession(h.db, sessionID)
	if err != nil || session == nil {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
		return
	}
	if !auth.TokensEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
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

	if err := auth.ChangePassword(h.db, userID, h.cookies.SessionID(r), passwordHash, newHash); err != nil {
		if errors.Is(err, auth.ErrSessionRejected) {
			writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Credentials changed; sign in again"))
			return
		}
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
	sessionID := h.cookies.SessionID(r)
	if sessionID == "" {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Authentication required"))
		return
	}
	session, err := auth.GetSession(h.db, sessionID)
	if err != nil || session == nil {
		writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Invalid or expired session"))
		return
	}
	if !auth.TokensEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		writeJSON(w, http.StatusForbidden, errResp("csrf_mismatch", "CSRF token mismatch"))
		return
	}

	var req struct {
		Confirm  string `json:"confirm"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Confirm != "DELETE" {
		writeJSON(w, http.StatusBadRequest, errResp("confirm_required", `Type DELETE to confirm: send {"confirm":"DELETE"}`))
		return
	}
	// Re-authenticate: a stolen or forgotten-open session alone must not be
	// able to destroy the account. Password accounts confirm the password;
	// accounts without one (Google) need a sign-in from the last 10 minutes.
	var passwordHash string
	if err := h.db.QueryRow("SELECT COALESCE(password_hash, '') FROM users WHERE id = ?", session.UserID).Scan(&passwordHash); err != nil {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Account not found"))
		return
	}
	if passwordHash != "" {
		if req.Password == "" || !h.checkPassword(req.Password, passwordHash) {
			writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Enter your current password to delete the account"))
			return
		}
	} else if age, err := auth.SessionAge(h.db, sessionID); err != nil || age > recentSignIn {
		writeJSON(w, http.StatusUnauthorized, errResp("reauth_required", "Sign in again, then delete the account within 10 minutes"))
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

	h.cookies.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Account deleted",
	})
}

// recentSignIn is how fresh a session must be to delete an account that has
// no password to confirm.
const recentSignIn = 10 * time.Minute

// errSignupClosed: CATO_SIGNUP does not allow another account.
var errSignupClosed = errors.New("signup closed")

// createUser inserts a user, honouring CATO_SIGNUP: unless signup is open,
// only the first account on an empty instance may be created. The emptiness
// check is part of the INSERT, so two racing first signups can't both win.
func (h *AuthHandler) createUser(userID, email, passwordHash, displayName, avatarURL string, googleSubject any) error {
	query := `INSERT INTO users (id, email, password_hash, display_name, avatar_url, google_subject)
		SELECT ?, ?, ?, ?, ?, ?`
	if !h.cfg.SignupIsOpen() {
		query += ` WHERE NOT EXISTS (SELECT 1 FROM users)`
	}
	var hash any
	if passwordHash != "" {
		hash = passwordHash
	}
	res, err := h.db.Exec(query, userID, email, hash, displayName, avatarURL, googleSubject)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errSignupClosed
	}
	return nil
}

// signupAllowed reports whether a new account could be created right now.
func (h *AuthHandler) signupAllowed() bool {
	if h.cfg.SignupIsOpen() {
		return true
	}
	var any int
	err := h.db.QueryRow("SELECT 1 FROM users LIMIT 1").Scan(&any)
	return err == sql.ErrNoRows
}

// handleConfig serves GET /api/config: public settings the login page needs.
func (h *AuthHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"google_enabled": h.googleCfg != nil,
		"signup":         h.cfg.SignupMode(),
		"signup_allowed": h.signupAllowed(),
		"dev":            h.cfg.Dev,
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

	if len(req.Password) > 72 {
		writeJSON(w, http.StatusBadRequest, errResp("weak_password", "Password must be at most 72 bytes"))
		return
	}
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to process password"))
		return
	}

	userID := uuid.New().String()
	if err := h.createUser(userID, req.Email, passwordHash, "", "", nil); err != nil {
		if errors.Is(err, errSignupClosed) {
			writeJSON(w, http.StatusForbidden, errResp("signup_closed", "Sign-up is closed on this server"))
			return
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			// Deliberately generic: don't confirm that the email has an account.
			writeJSON(w, http.StatusBadRequest, errResp("signup_failed", "Couldn't create an account with those details. If you already have one, sign in instead."))
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
	auth.NoteSessionClient(h.db, session.ID, r.UserAgent())

	h.cookies.SetSession(w, session.ID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user_id":       userID,
		"email":         req.Email,
		"authenticated": true,
		"csrf_token":    session.CSRFToken,
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

	// Per-account backoff (any email, existing or not) on top of the per-IP
	// limiter: guessing one account's password from many addresses slows down.
	if wait := h.loginThrottle.Wait(req.Email); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second)/time.Second)+1))
		writeJSON(w, http.StatusTooManyRequests, errResp("rate_limited", "Too many failed sign-ins for this account. Try again shortly."))
		return
	}

	var userID, passwordHash, displayName string
	var disabled int
	err := h.db.QueryRow(
		"SELECT id, COALESCE(password_hash, ''), COALESCE(display_name, ''), disabled FROM users WHERE email = ?",
		req.Email,
	).Scan(&userID, &passwordHash, &displayName, &disabled)
	if err != nil && err != sql.ErrNoRows {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Login failed"))
		return
	}

	// Unknown emails and password-less accounts still pay for a bcrypt
	// compare, so timing doesn't reveal which emails have accounts. The
	// password is checked before "disabled", which only someone who knows
	// the password gets to see.
	hash := passwordHash
	if hash == "" {
		hash = auth.DummyHash()
	}
	if !h.checkPassword(req.Password, hash) || passwordHash == "" {
		h.loginThrottle.Failure(req.Email)
		writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Invalid email or password"))
		return
	}

	if disabled != 0 {
		writeJSON(w, http.StatusForbidden, errResp("account_disabled", "Account is disabled"))
		return
	}
	h.loginThrottle.Success(req.Email)

	session, err := auth.CreatePasswordSession(h.db, userID, passwordHash)
	if err != nil {
		if errors.Is(err, auth.ErrSessionRejected) {
			writeJSON(w, http.StatusUnauthorized, errResp("invalid_credentials", "Invalid email or password"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create session"))
		return
	}
	auth.NoteSessionClient(h.db, session.ID, r.UserAgent())

	h.cookies.SetSession(w, session.ID)
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

	sessionID := h.cookies.SessionID(r)
	if sessionID != "" {
		auth.DeleteSession(h.db, sessionID)
	}

	h.cookies.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Logged out",
	})
}

// authErrorPage is what a browser sees when the Google sign-in redirect
// flow fails: these are top-level navigations, so JSON would be shown raw.
var authErrorPage = template.Must(template.New("auth-error").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign-in problem · Cato</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#1a1a2e;color:#eee;font:16px/1.5 system-ui,sans-serif}
main{max-width:28rem;padding:2rem;text-align:center}a{color:#e94560}</style></head>
<body><main><h1>Couldn't sign you in</h1><p>{{.}}</p><p><a href="/login">Back to sign in</a></p></main></body></html>
`))

func writeAuthErrorPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = authErrorPage.Execute(w, message)
}

func (h *AuthHandler) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if h.googleCfg == nil {
		writeAuthErrorPage(w, http.StatusServiceUnavailable, "Google sign-in is not set up on this server.")
		return
	}

	state := auth.RandomToken(16)

	// Store state in a dedicated short-lived cookie for CSRF protection.
	// (This must NOT touch the session cookie — overwriting it here used to
	// log out any already signed-in user who clicked the Google button.)
	h.cookies.SetOAuthState(w, state)

	url := h.googleCfg.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *AuthHandler) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if h.googleCfg == nil {
		writeAuthErrorPage(w, http.StatusServiceUnavailable, "Google sign-in is not set up on this server.")
		return
	}

	state := r.URL.Query().Get("state")
	if stateCookie := h.cookies.OAuthState(r); state == "" || stateCookie != state {
		writeAuthErrorPage(w, http.StatusBadRequest, "The sign-in link expired or was opened in a different browser. Please try again.")
		return
	}

	h.cookies.ClearOAuthState(w)

	code := r.URL.Query().Get("code")
	if code == "" {
		writeAuthErrorPage(w, http.StatusBadRequest, "Google didn't complete the sign-in. Please try again.")
		return
	}

	googleUser, err := auth.FetchGoogleUser(r.Context(), h.googleCfg, code)
	if err != nil {
		writeAuthErrorPage(w, http.StatusInternalServerError, "We couldn't verify your Google account. Please try again.")
		return
	}

	userID, err := h.findOrCreateGoogleUser(googleUser)
	if err != nil {
		switch {
		case errors.Is(err, errGoogleEmailConflict):
			writeAuthErrorPage(w, http.StatusConflict, "An account already uses this email. Sign in with its existing method; automatic Google linking is not supported.")
		case errors.Is(err, errGoogleDisabled):
			writeAuthErrorPage(w, http.StatusForbidden, "This account is disabled.")
		case errors.Is(err, errSignupClosed):
			writeAuthErrorPage(w, http.StatusForbidden, "Sign-up is closed on this server.")
		default:
			writeAuthErrorPage(w, http.StatusInternalServerError, "Something went wrong while signing you in. Please try again.")
		}
		return
	}

	session, err := auth.CreateSession(h.db, userID)
	if err != nil {
		writeAuthErrorPage(w, http.StatusInternalServerError, "Something went wrong while signing you in. Please try again.")
		return
	}
	auth.NoteSessionClient(h.db, session.ID, r.UserAgent())

	h.cookies.SetSession(w, session.ID)
	http.Redirect(w, r, "/library", http.StatusFound)
}

var errGoogleEmailConflict = errors.New("email belongs to another account")
var errGoogleDisabled = errors.New("account disabled")

func (h *AuthHandler) findOrCreateGoogleUser(gu *auth.GoogleUser) (string, error) {
	if err := auth.ValidateGoogleUser(gu); err != nil {
		return "", err
	}
	var userID string
	var disabled int
	err := h.db.QueryRow("SELECT id, disabled FROM users WHERE google_subject = ?", gu.Sub).Scan(&userID, &disabled)
	if err == nil {
		if disabled != 0 {
			return "", errGoogleDisabled
		}
		return userID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	email := strings.ToLower(strings.TrimSpace(gu.Email))
	err = h.db.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
	if err == nil {
		return "", errGoogleEmailConflict
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	userID = uuid.New().String()
	err = h.createUser(userID, email, "", gu.Name, gu.Picture, gu.Sub)
	if errors.Is(err, errSignupClosed) {
		return "", err
	}
	if err != nil {
		// A concurrent callback/signup may have won the unique-key race.
		if strings.Contains(err.Error(), "UNIQUE") {
			var existing string
			var disabled int
			if lookupErr := h.db.QueryRow("SELECT id, disabled FROM users WHERE google_subject = ?", gu.Sub).Scan(&existing, &disabled); lookupErr == nil {
				if disabled != 0 {
					return "", errGoogleDisabled
				}
				return existing, nil
			}
			return "", errGoogleEmailConflict
		}
		return "", err
	}
	return userID, nil
}
