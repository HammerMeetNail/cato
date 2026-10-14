package v1

import (
	"encoding/json"
	"errors"
	"net/http"

	"cato/internal/imports"
	"cato/internal/library"
)

func (a *API) registerImports(route func(string, http.HandlerFunc)) {
	route("GET /api/v1/import", a.importStatus)
	route("POST /api/v1/import/csv/preview", a.previewCSV)
	route("POST /api/v1/import/json/preview", a.previewJSON)
	route("POST /api/v1/import/steam/preview", a.previewSteam)
	route("PATCH /api/v1/import/steam", a.steamSync)
	route("DELETE /api/v1/import/steam", a.unlinkSteam)
	route("POST /api/v1/import/ra/preview", a.previewRA)
	route("POST /api/v1/import/commit", a.commitImport)
	route("POST /api/v1/import/batches/{id}/undo", a.undoImport)
}

// importFail explains source problems (a private Steam profile, a bad file)
// as 400s; everything else is fail.
func importFail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *library.ValidationError
	switch {
	case errors.As(err, &ve), errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrUndoExpired):
		fail(w, r, err)
	case errors.Is(err, imports.ErrSteamNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "not_configured", err.Error(), nil)
	default:
		writeError(w, http.StatusBadRequest, "import_failed", err.Error(), nil)
	}
}

func (a *API) importStatus(w http.ResponseWriter, r *http.Request) {
	link, err := a.Imports.SteamLinkOf(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	batches, err := a.Library.ImportBatches(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	var raUser string
	a.DB.QueryRowContext(r.Context(), `SELECT ra_user FROM ra_links WHERE user_id = ?`, user(r)).Scan(&raUser)
	writeJSON(w, http.StatusOK, map[string]any{
		"steam_configured": a.Imports.Steam != nil && a.Imports.Steam.Key != "",
		"steam":            link,
		"ra_user":          raUser,
		"batches":          batches,
		"presets":          []imports.Preset{imports.PresetGrouvee, imports.PresetHLTB, imports.PresetIB, imports.PresetBackloggd, imports.PresetCato, imports.PresetGeneric},
		"fields":           imports.Fields,
	})
}

func (a *API) previewCSV(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CSV     string           `json:"csv"`
		Mapping *imports.Mapping `json:"mapping"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := imports.ParseCSV([]byte(in.CSV))
	if err != nil {
		importFail(w, r, err)
		return
	}
	preset, mapping := imports.Detect(t.Headers)
	if in.Mapping != nil && in.Mapping.Columns != nil {
		mapping = *in.Mapping
	}
	if _, ok := mapping.Columns["title"]; !ok {
		writeJSON(w, http.StatusOK, map[string]any{"headers": t.Headers, "preset": preset, "mapping": mapping, "rows": len(t.Rows), "items": []any{},
			"message": "Choose which column has the game titles."})
		return
	}
	cands, err := a.Imports.Matcher.Match(r.Context(), user(r), t.Apply(mapping))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"headers": t.Headers, "preset": preset, "mapping": mapping, "rows": len(t.Rows), "items": cands})
}

func (a *API) previewJSON(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Data json.RawMessage `json:"data"`
	}
	if !decode(w, r, &in) {
		return
	}
	raw := []byte(in.Data)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s) // the file's text, sent as a string
	}
	ex, rows, err := imports.ParseCatoJSON(raw)
	if err != nil {
		importFail(w, r, err)
		return
	}
	cands, err := a.Imports.Matcher.Match(r.Context(), user(r), rows)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cands, "extras": imports.Extras{Reviews: ex.Reviews, Lists: ex.Lists}})
}

func (a *API) previewSteam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Profile string `json:"profile"`
	}
	if !decode(w, r, &in) {
		return
	}
	link, cands, err := a.Imports.SteamPreview(r.Context(), user(r), in.Profile)
	if err != nil {
		importFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steam": link, "items": cands})
}

func (a *API) steamSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sync bool `json:"sync"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Imports.SetSteamSync(r.Context(), user(r), in.Sync); err != nil {
		importFail(w, r, err)
		return
	}
	a.importStatus(w, r)
}

func (a *API) unlinkSteam(w http.ResponseWriter, r *http.Request) {
	if err := a.Imports.UnlinkSteam(r.Context(), user(r)); err != nil {
		fail(w, r, err)
		return
	}
	a.importStatus(w, r)
}

func (a *API) previewRA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User     string `json:"user"`
		Key      string `json:"key"`
		Remember bool   `json:"remember"`
	}
	if !decode(w, r, &in) {
		return
	}
	cands, err := a.Imports.RAPreview(r.Context(), user(r), in.User, in.Key, in.Remember)
	if err != nil {
		importFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cands})
}

func (a *API) commitImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source  string                `json:"source"`
		Entries []imports.CommitEntry `json:"entries"`
		Extras  *imports.Extras       `json:"extras"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Source {
	case "csv", "cato-json", "steam", "retroachievements", "grouvee", "hltb", "infinite_backlog", "backloggd", "cato", "generic":
	default:
		writeError(w, http.StatusBadRequest, "invalid_source", "Unknown import source", map[string]string{"source": "unknown"})
		return
	}
	res, err := a.Imports.Commit(r.Context(), user(r), in.Source, in.Entries, in.Extras)
	if err != nil {
		importFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) undoImport(w http.ResponseWriter, r *http.Request) {
	n, err := a.Library.UndoImport(r.Context(), user(r), r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	rev, _ := a.Library.Rev(r.Context(), user(r))
	writeJSON(w, http.StatusOK, map[string]any{"restored": n, "library_rev": rev})
}
