package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"cato/internal/auth"
	"cato/internal/webauthn"
)

// Passkeys (plan §8.3). Sign-in is public; managing passkeys needs a
// session (see v1). The relying party is this server's host name, which
// can't be an IP address.

func (h *AuthHandler) handlePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}
	rp, err := webauthn.RPFor(h.cfg.BaseURL, h.cfg.HTTPS(), r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("passkeys_unavailable", err.Error()))
		return
	}
	ch := h.passkeys.New("")
	if ch == nil {
		writeJSON(w, http.StatusServiceUnavailable, errResp("busy", "Try again in a minute"))
		return
	}
	// Discoverable credentials: the browser offers the passkeys it has.
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": map[string]any{
		"challenge": webauthn.Encode(ch), "rpId": rp.ID, "userVerification": "preferred", "timeout": 120000,
	}})
}

func (h *AuthHandler) handlePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}
	rp, err := webauthn.RPFor(h.cfg.BaseURL, h.cfg.HTTPS(), r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("passkeys_unavailable", err.Error()))
		return
	}
	var req struct {
		ID       string `json:"id"`
		Response struct {
			ClientDataJSON    string `json:"clientDataJSON"`
			AuthenticatorData string `json:"authenticatorData"`
			Signature         string `json:"signature"`
		} `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}
	cd, e1 := webauthn.Decode(req.Response.ClientDataJSON)
	ad, e2 := webauthn.Decode(req.Response.AuthenticatorData)
	sig, e3 := webauthn.Decode(req.Response.Signature)
	credID, e4 := webauthn.Decode(req.ID)
	if errors.Join(e1, e2, e3, e4) != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid passkey response"))
		return
	}
	challenge, err := h.passkeys.Take(cd, "")
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errResp("passkey_failed", err.Error()))
		return
	}
	var userID string
	var cred webauthn.Credential
	var count int64
	var disabled int
	err = h.db.QueryRow(`SELECT c.user_id, c.public_key, c.algorithm, c.sign_count, u.disabled FROM webauthn_credentials c
		JOIN users u ON u.id = c.user_id WHERE c.id = ?`, webauthn.Encode(credID)).Scan(&userID, &cred.PublicKey, &cred.Algorithm, &count, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusUnauthorized, errResp("passkey_unknown", "This passkey isn't registered here. Sign in with your password and add it in Settings."))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Sign-in failed"))
		return
	}
	cred.SignCount = uint32(count)
	newCount, err := rp.Login(challenge, cred, cd, ad, sig)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errResp("passkey_failed", err.Error()))
		return
	}
	if disabled != 0 {
		writeJSON(w, http.StatusForbidden, errResp("account_disabled", "Account is disabled"))
		return
	}
	h.db.Exec(`UPDATE webauthn_credentials SET sign_count = ?, last_used_at = ? WHERE id = ?`, newCount, time.Now().UTC().Format(time.RFC3339), webauthn.Encode(credID))
	session, err := auth.CreateSession(h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to create session"))
		return
	}
	auth.NoteSessionClient(h.db, session.ID, r.UserAgent())
	h.cookies.SetSession(w, session.ID)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user_id": userID, "csrf_token": session.CSRFToken})
}
