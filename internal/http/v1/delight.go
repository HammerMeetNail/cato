package v1

import (
	"net/http"
	"strconv"
	"time"

	"cato/internal/library"
)

func (a *API) registerDelight(route func(string, http.HandlerFunc)) {
	route("GET /api/v1/year/{year}", a.yearInReview)
	route("GET /api/v1/goals", a.listGoals)
	route("POST /api/v1/goals", a.addGoal)
	route("DELETE /api/v1/goals/{id}", a.deleteGoal)
}

func (a *API) yearInReview(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 1970 || year > 2100 {
		writeError(w, http.StatusBadRequest, "invalid_year", "Invalid year", map[string]string{"year": "invalid"})
		return
	}
	rv, err := a.Library.YearInReview(r.Context(), user(r), year)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}

func (a *API) listGoals(w http.ResponseWriter, r *http.Request) {
	year := queryInt(r, "year", time.Now().Year())
	gs, err := a.Library.Goals(r.Context(), user(r), year)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": gs, "year": year})
}

func (a *API) addGoal(w http.ResponseWriter, r *http.Request) {
	var in library.Goal
	if !decode(w, r, &in) {
		return
	}
	if in.Year == 0 {
		in.Year = time.Now().Year()
	}
	g, err := a.Library.AddGoal(r.Context(), user(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"goal": g})
}

func (a *API) deleteGoal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Library.DeleteGoal(r.Context(), user(r), id); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
