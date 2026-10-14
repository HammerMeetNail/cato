package auth

import (
	"crypto/subtle"
	"net/http"
	"time"
)

const (
	sessionCookie       = "cato_session"
	secureSessionCookie = "__Host-cato_session"
	oauthCookie         = "cato_oauth_state"
	secureOAuthCookie   = "__Host-cato_oauth_state"
	oauthCookiePath     = "/api/auth/google"
)

// Cookies names and flags Cato's cookies. When Secure (HTTPS deployments)
// every cookie uses the __Host- prefix: browsers then require Secure,
// Path=/ and no Domain, so a sibling subdomain can neither plant nor
// overwrite it. Unprefixed cookies are ignored in that mode.
type Cookies struct {
	Secure bool
}

func (c Cookies) SessionName() string {
	if c.Secure {
		return secureSessionCookie
	}
	return sessionCookie
}

func (c Cookies) SessionID(r *http.Request) string {
	cookie, err := r.Cookie(c.SessionName())
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (c Cookies) SetSession(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.SessionName(),
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Secure,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

func (c Cookies) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.SessionName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Secure,
		MaxAge:   -1,
	})
}

func (c Cookies) oauthName() (string, string) {
	if c.Secure {
		return secureOAuthCookie, "/"
	}
	return oauthCookie, oauthCookiePath
}

// SetOAuthState stores the Google OAuth state in its own short-lived cookie
// (never the session cookie: overwriting that would sign out a user who
// clicks the Google button while signed in).
func (c Cookies) SetOAuthState(w http.ResponseWriter, state string) {
	name, path := c.oauthName()
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    state,
		Path:     path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Secure,
		MaxAge:   600,
	})
}

func (c Cookies) ClearOAuthState(w http.ResponseWriter) {
	name, path := c.oauthName()
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Secure,
		MaxAge:   -1,
	})
}

func (c Cookies) OAuthState(r *http.Request) string {
	name, _ := c.oauthName()
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// TokensEqual compares secret tokens (CSRF) in constant time and exactly:
// no case folding, no early exit on the first differing byte.
func TokensEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
