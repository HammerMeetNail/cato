package v1

import (
	"errors"
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

// discoverQuery: popular catalog games with art that aren't in your library.
const discoverPopular = `SELECT g.id, g.name, g.cover_url, g.local_cover_path, g.first_release_date
	FROM games g
	WHERE g.local_cover_path != ''
	  AND NOT EXISTS (SELECT 1 FROM library_items li WHERE li.user_id = ? AND li.game_id = g.id)
	ORDER BY g.popularity_score DESC, g.rating_count DESC, g.name
	LIMIT ?`

func (a *API) discover(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.QueryContext(r.Context(), discoverPopular, user(r), min(max(queryInt(r, "limit", 18), 1), 60))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer rows.Close()
	popular := []CatalogResult{}
	for rows.Next() {
		var c CatalogResult
		var remote, local string
		if err := rows.Scan(&c.ID, &c.Name, &remote, &local, &c.ReleaseDate); err != nil {
			fail(w, r, err)
			return
		}
		c.Cover = coverOf(local, remote)
		c.Platforms = []string{}
		popular = append(popular, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shelves": []map[string]any{
		{"id": "popular", "title": "Popular", "items": popular},
	}})
}
