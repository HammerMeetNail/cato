package v1

import (
	"errors"
	"net/http"
	"strings"

	"cato/internal/imports"
	"cato/internal/push"
	"cato/internal/webauthn"
)

func (a *API) registerReach(route func(string, http.HandlerFunc)) {
	route("GET /api/v1/passkeys", a.listPasskeys)
	route("POST /api/v1/passkeys/begin", a.beginPasskey)
	route("POST /api/v1/passkeys", a.addPasskey)
	route("DELETE /api/v1/passkeys/{id}", a.deletePasskey)

	route("GET /api/v1/push", a.pushStatus)
	route("POST /api/v1/push/subscriptions", a.pushSubscribe)
	route("DELETE /api/v1/push/subscriptions", a.pushUnsubscribe)
	route("POST /api/v1/push/test", a.pushTest)

	route("GET /api/v1/barcode/{code}", a.barcode)
	route("PUT /api/v1/barcode/{code}", a.confirmBarcode)
}

// ---- passkeys ---------------------------------------------------------------

func (a *API) listPasskeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT id, name, created_at, COALESCE(last_used_at, '') FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at`, user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, created, used string
		rows.Scan(&id, &name, &created, &used)
		items = append(items, map[string]any{"id": id, "name": name, "created_at": created, "last_used_at": used})
	}
	_, rpErr := webauthn.RPFor(a.Cfg.BaseURL, a.Cfg.HTTPS(), r)
	resp := map[string]any{"items": items, "available": rpErr == nil}
	if rpErr != nil {
		resp["reason"] = rpErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) beginPasskey(w http.ResponseWriter, r *http.Request) {
	rp, err := webauthn.RPFor(a.Cfg.BaseURL, a.Cfg.HTTPS(), r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkeys_unavailable", err.Error(), nil)
		return
	}
	var email, name string
	a.DB.QueryRowContext(r.Context(), `SELECT email, display_name FROM users WHERE id = ?`, user(r)).Scan(&email, &name)
	if name == "" {
		name = email
	}
	ch := a.Passkeys.New(user(r))
	if ch == nil {
		writeError(w, http.StatusServiceUnavailable, "busy", "Try again in a minute", nil)
		return
	}
	exclude := []map[string]any{}
	rows, err := a.DB.QueryContext(r.Context(), `SELECT id, transports FROM webauthn_credentials WHERE user_id = ?`, user(r))
	if err == nil {
		for rows.Next() {
			var id, tr string
			rows.Scan(&id, &tr)
			e := map[string]any{"type": "public-key", "id": id}
			if tr != "" {
				e["transports"] = strings.Split(tr, ",")
			}
			exclude = append(exclude, e)
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": map[string]any{
		"challenge": webauthn.Encode(ch),
		"rp":        map[string]any{"id": rp.ID, "name": rp.Name},
		"user":      map[string]any{"id": webauthn.Encode([]byte(user(r))), "name": email, "displayName": name},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": webauthn.AlgES256}, {"type": "public-key", "alg": webauthn.AlgEdDSA}, {"type": "public-key", "alg": webauthn.AlgRS256},
		},
		"authenticatorSelection": map[string]any{"residentKey": "required", "requireResidentKey": true, "userVerification": "preferred"},
		"attestation":            "none",
		"excludeCredentials":     exclude,
		"timeout":                120000,
	}})
}

func (a *API) addPasskey(w http.ResponseWriter, r *http.Request) {
	rp, err := webauthn.RPFor(a.Cfg.BaseURL, a.Cfg.HTTPS(), r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkeys_unavailable", err.Error(), nil)
		return
	}
	var in struct {
		Name     string `json:"name"`
		Response struct {
			ClientDataJSON    string   `json:"clientDataJSON"`
			AttestationObject string   `json:"attestationObject"`
			Transports        []string `json:"transports"`
		} `json:"response"`
	}
	if !decode(w, r, &in) {
		return
	}
	cd, e1 := webauthn.Decode(in.Response.ClientDataJSON)
	att, e2 := webauthn.Decode(in.Response.AttestationObject)
	if e1 != nil || e2 != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid passkey response", nil)
		return
	}
	challenge, err := a.Passkeys.Take(cd, user(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey_failed", err.Error(), nil)
		return
	}
	cred, err := rp.Register(challenge, cd, att)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey_failed", err.Error(), nil)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Passkey"
	}
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	var tr []string
	for _, t := range in.Response.Transports {
		if len(t) < 20 && len(tr) < 6 {
			tr = append(tr, t)
		}
	}
	if _, err := a.DB.ExecContext(r.Context(), `INSERT INTO webauthn_credentials (id, user_id, public_key, algorithm, sign_count, name, transports) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		webauthn.Encode(cred.ID), user(r), cred.PublicKey, cred.Algorithm, cred.SignCount, name, strings.Join(tr, ",")); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "passkey_exists", "That passkey is already registered", nil)
			return
		}
		fail(w, r, err)
		return
	}
	a.listPasskeys(w, r)
}

func (a *API) deletePasskey(w http.ResponseWriter, r *http.Request) {
	res, err := a.DB.ExecContext(r.Context(), `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, r.PathValue("id"), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "No such passkey", nil)
		return
	}
	a.listPasskeys(w, r)
}

// ---- push -------------------------------------------------------------------

func (a *API) pushStatus(w http.ResponseWriter, r *http.Request) {
	key, err := a.Notify.PublicKey(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	devices, err := a.Notify.Devices(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_key": key, "devices": devices, "email": a.Notify.EmailEnabled(),
		"price_alerts": a.Cfg.ITADKey != "", "steam": a.Cfg.SteamKey != ""})
}

func (a *API) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Notify.Subscribe(r.Context(), user(r), push.Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}, r.UserAgent()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_subscription", err.Error(), nil)
		return
	}
	a.pushStatus(w, r)
}

func (a *API) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Notify.Unsubscribe(r.Context(), user(r), in.Endpoint); err != nil {
		fail(w, r, err)
		return
	}
	a.pushStatus(w, r)
}

func (a *API) pushTest(w http.ResponseWriter, r *http.Request) {
	if err := a.Notify.Test(r.Context(), user(r)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// ---- barcodes ---------------------------------------------------------------

func (a *API) barcode(w http.ResponseWriter, r *http.Request) {
	res, err := a.Barcode.Lookup(r.Context(), user(r), r.PathValue("code"))
	if errors.Is(err, imports.ErrBadCode) {
		writeError(w, http.StatusBadRequest, "invalid_code", err.Error(), map[string]string{"code": "invalid"})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "lookup_failed", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) confirmBarcode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GameID int64 `json:"game_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Barcode.Confirm(r.Context(), r.PathValue("code"), in.GameID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_code", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "game_id": in.GameID})
}
