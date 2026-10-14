package v1

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"cato/internal/auth"
	"cato/internal/library"
)

func (a *API) getMe(w http.ResponseWriter, r *http.Request) {
	uid := user(r)
	var email, name string
	var hasPassword bool
	if err := a.DB.QueryRowContext(r.Context(), `SELECT email, display_name, COALESCE(password_hash, '') != '' FROM users WHERE id = ?`, uid).
		Scan(&email, &name, &hasPassword); err != nil {
		fail(w, r, err)
		return
	}
	prefs, err := a.Library.GetPrefs(r.Context(), uid)
	if err != nil {
		fail(w, r, err)
		return
	}
	rev, err := a.Library.Rev(r.Context(), uid)
	if err != nil {
		fail(w, r, err)
		return
	}
	me := map[string]any{"id": uid, "email": email, "display_name": name, "has_password": hasPassword}
	unread := 0
	if a.Social != nil {
		if p, err := a.Social.MyProfile(r.Context(), uid); err == nil {
			me["username"], me["avatar"], me["role"], me["visibility"] = p.Username, p.Avatar, p.Role, p.Visibility
		}
		unread, _ = a.Social.Unread(r.Context(), uid)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        me,
		"unread":      unread,
		"csrf_token":  auth.GetCSRFToken(r.Context()),
		"prefs":       prefs,
		"library_rev": rev,
	})
}

func (a *API) patchPrefs(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	prefs, err := a.Library.UpdatePrefs(r.Context(), user(r), raw)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prefs": prefs})
}

// filterFrom reads the shared library filter parameters.
func filterFrom(r *http.Request) library.Filter {
	q := r.URL.Query()
	var statuses []string
	for _, raw := range append(q["status"], q["shelf"]...) {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				statuses = append(statuses, part)
			}
		}
	}
	f := library.Filter{
		Statuses:      statuses,
		Tags:          q["tag"],
		TagOp:         q.Get("tag_op"),
		Platform:      q.Get("platform"),
		OwnedPlatform: q.Get("owned_platform"),
		Format:        q.Get("format"),
		Q:             q.Get("q"),
		ReleaseFrom:   library.ParseDate(q.Get("release_from"), false),
		ReleaseTo:     library.ParseDate(q.Get("release_to"), true),
	}
	if y := library.ParseYear(q.Get("year_from"), false); y != 0 {
		f.ReleaseFrom = y
	}
	if y := library.ParseYear(q.Get("year_to"), true); y != 0 {
		f.ReleaseTo = y
	}
	return f
}

func (a *API) listLibrary(w http.ResponseWriter, r *http.Request) {
	if a.notModified(w, r, "list") {
		return
	}
	q := library.ListQuery{
		Filter: filterFrom(r),
		Sort:   r.URL.Query().Get("sort"),
		Cursor: r.URL.Query().Get("cursor"),
		Offset: queryInt(r, "offset", 0),
		Limit:  queryInt(r, "limit", 60),
	}
	page, err := a.Library.List(r.Context(), user(r), q)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) libraryIndex(w http.ResponseWriter, r *http.Request) {
	if a.notModified(w, r, "index") {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	idx, err := a.Library.Index(r.Context(), user(r), since)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

func (a *API) libraryCounts(w http.ResponseWriter, r *http.Request) {
	if a.notModified(w, r, "counts") {
		return
	}
	counts, err := a.Library.Counts(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func (a *API) libraryTags(w http.ResponseWriter, r *http.Request) {
	tags, err := a.Library.Tags(r.Context(), user(r), r.URL.Query().Get("q"), queryInt(r, "limit", 200))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tags})
}

func (a *API) libraryPlatforms(w http.ResponseWriter, r *http.Request) {
	list, err := a.Library.OwnedPlatforms(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *API) getItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := a.Library.Get(r.Context(), user(r), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// writeResult answers a mutation (201 when it created the item).
func writeResult(w http.ResponseWriter, res *library.WriteResult) {
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

func (a *API) putItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var ch library.Change
	if !decode(w, r, &ch) {
		return
	}
	// Editions roll up to their base game; the edition played is recorded
	// on the playthrough (plan §6.2).
	base, err := a.Games.BaseGame(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if base != id {
		edition := id
		ch.EditionGameID = &edition
	}
	res, err := a.Library.Upsert(r.Context(), user(r), base, ch)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeResult(w, res)
}

func (a *API) patchItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var ch library.Change
	if !decode(w, r, &ch) {
		return
	}
	res, err := a.Library.Patch(r.Context(), user(r), id, ch)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeResult(w, res)
}

func (a *API) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	undo, err := a.Library.Delete(r.Context(), user(r), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rev, _ := a.Library.Rev(r.Context(), user(r))
	writeJSON(w, http.StatusOK, map[string]any{"undo_id": undo, "library_rev": rev})
}

func (a *API) bulk(w http.ResponseWriter, r *http.Request) {
	var act library.BulkAction
	if !decode(w, r, &act) {
		return
	}
	res, err := a.Library.Bulk(r.Context(), user(r), act)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) undo(w http.ResponseWriter, r *http.Request) {
	ids, err := a.Library.Undo(r.Context(), user(r), r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	rev, _ := a.Library.Rev(r.Context(), user(r))
	writeJSON(w, http.StatusOK, map[string]any{"game_ids": ids, "library_rev": rev})
}

func (a *API) listPlaythroughs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pts, err := a.Library.Playthroughs(r.Context(), user(r), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": pts})
}

func (a *API) exportJSON(w http.ResponseWriter, r *http.Request) {
	items, err := a.Library.ExportAll(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	prefs, _ := a.Library.GetPrefs(r.Context(), user(r))
	w.Header().Set("Content-Disposition", `attachment; filename="cato-export.json"`)
	writeJSON(w, http.StatusOK, map[string]any{"format": "cato-export", "version": 1, "prefs": prefs, "library": items})
}

func (a *API) exportCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="cato-library.csv"`)
	if err := a.Library.WriteCSV(r.Context(), user(r), w); err != nil {
		fail(w, r, err)
	}
}
