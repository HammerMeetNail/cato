package v1

import (
	"net/http"
	"strconv"
	"time"

	"cato/internal/library"
)

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	gameID, _ := strconv.ParseInt(q.Get("game_id"), 10, 64)
	items, err := a.Library.Sessions(r.Context(), user(r), library.SessionRange{
		From: q.Get("from"), To: q.Get("to"), GameID: gameID, Limit: queryInt(r, "limit", 500),
		IncludeAdjustments: q.Get("all") == "1",
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type sessionInput struct {
	GameID   int64  `json:"game_id"`
	Minutes  int64  `json:"minutes"`
	PlayedOn string `json:"played_on"`
	Note     string `json:"note"`
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var in sessionInput
	if !decode(w, r, &in) {
		return
	}
	res, err := a.Library.LogSession(r.Context(), user(r), in.GameID, in.Minutes, in.PlayedOn, in.Note)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (a *API) patchSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		PlayedOn *string `json:"played_on"`
		Minutes  *int64  `json:"minutes"`
		Note     *string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := a.Library.UpdateSession(r.Context(), user(r), id, in.PlayedOn, in.Minutes, in.Note)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := a.Library.DeleteSession(r.Context(), user(r), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) patchPlaythrough(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var ch library.PlaythroughChange
	if !decode(w, r, &ch) {
		return
	}
	res, err := a.Library.UpdatePlaythrough(r.Context(), user(r), id, ch)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) deletePlaythrough(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := a.Library.DeletePlaythrough(r.Context(), user(r), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) getTimer(w http.ResponseWriter, r *http.Request) {
	t, err := a.Library.GetTimer(r.Context(), user(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timer": t, "server_time": time.Now().UTC().Format(time.RFC3339)})
}

func (a *API) startTimer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GameID int64 `json:"game_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := a.Library.StartTimer(r.Context(), user(r), in.GameID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timer": t, "server_time": time.Now().UTC().Format(time.RFC3339)})
}

func (a *API) stopTimer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Log      bool   `json:"log"`
		Minutes  int64  `json:"minutes"`
		PlayedOn string `json:"played_on"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := a.Library.StopTimer(r.Context(), user(r), in.Log, in.Minutes, in.PlayedOn)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	if a.notModified(w, r, "stats") {
		return
	}
	st, err := a.Library.StatsFor(r.Context(), user(r), queryInt(r, "year", 0), r.URL.Query().Get("today"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// home is everything the Home screen shows, in one request.
func (a *API) home(w http.ResponseWriter, r *http.Request) {
	ctx, uid := r.Context(), user(r)
	playing, err := a.Library.List(ctx, uid, library.ListQuery{Filter: library.Filter{Statuses: []string{library.StatusPlaying, library.StatusPaused}}, Limit: 24})
	if err != nil {
		fail(w, r, err)
		return
	}
	prefs, err := a.Library.GetPrefs(ctx, uid)
	if err != nil {
		fail(w, r, err)
		return
	}
	upNext, err := a.Library.UpNext(ctx, uid, prefs.UpNext, 3)
	if err != nil {
		fail(w, r, err)
		return
	}
	recent, err := a.Library.Sessions(ctx, uid, library.SessionRange{Limit: 8})
	if err != nil {
		fail(w, r, err)
		return
	}
	timer, err := a.Library.GetTimer(ctx, uid)
	if err != nil {
		fail(w, r, err)
		return
	}
	counts, err := a.Library.Counts(ctx, uid)
	if err != nil {
		fail(w, r, err)
		return
	}
	resp := map[string]any{
		"playing": playing.Items, "up_next": upNext, "recent": recent, "timer": timer, "counts": counts,
		"onboarding": counts["all"] == 0 && !prefs.OnboardingDone,
	}
	if prefs.Streaks {
		cur, longest, err := a.Library.Streaks(ctx, uid, r.URL.Query().Get("today"))
		if err == nil {
			resp["streak"] = map[string]int{"current": cur, "longest": longest}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
