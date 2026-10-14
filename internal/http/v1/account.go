package v1

import (
	"errors"

	"cato/internal/discover"
	"net/http"

	"cato/internal/auth"
)

func currentSession(r *http.Request) string {
	if s, ok := r.Context().Value(auth.SessionKey).(*auth.Session); ok && s != nil {
		return s.ID
	}
	return ""
}

func (a *API) devices(w http.ResponseWriter, r *http.Request) {
	list, err := auth.ListDevices(a.DB, user(r), currentSession(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *API) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := auth.RevokeDevice(a.DB, user(r), id); err != nil {
		if errors.Is(err, auth.ErrNoSuchDevice) {
			writeError(w, http.StatusNotFound, "not_found", "No such device", nil)
			return
		}
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) revokeOtherDevices(w http.ResponseWriter, r *http.Request) {
	n, err := auth.RevokeOtherDevices(a.DB, user(r), currentSession(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

func (a *API) discover(w http.ResponseWriter, r *http.Request) {
	shelves, err := a.Discover.Shelves(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if shelves == nil {
		shelves = []discover.Shelf{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shelves": shelves, "attribution": "Game data from IGDB"})
}

// upNext ranks the backlog: ?fit=tonight|weekend|long, ?n= (≤ 10; "Surprise
// me" picks among the top 10).
func (a *API) upNext(w http.ResponseWriter, r *http.Request) {
	fit := discover.Fit(r.URL.Query().Get("fit"))
	switch fit {
	case "", "tonight", "weekend", "long":
	default:
		writeError(w, http.StatusBadRequest, "invalid_fit", "fit is tonight, weekend or long", map[string]string{"fit": "invalid"})
		return
	}
	picks, err := a.Discover.UpNext(r.Context(), user(r), min(max(queryInt(r, "n", 3), 1), 10), fit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": picks})
}
