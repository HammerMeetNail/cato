package v1

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"cato/internal/social"
)

// registerSocial mounts the people endpoints (plan §7).
func (a *API) registerSocial(route func(string, http.HandlerFunc)) {
	route("GET /api/v1/me/profile", a.myProfile)
	route("PATCH /api/v1/me/profile", a.editProfile)
	route("PUT /api/v1/me/avatar", a.setAvatar)
	route("DELETE /api/v1/me/avatar", a.clearAvatar)

	route("GET /api/v1/users", a.searchPeople)
	route("GET /api/v1/users/{username}", a.profile)
	route("POST /api/v1/users/{username}/follow", a.follow)
	route("DELETE /api/v1/users/{username}/follow", a.unfollow)
	route("POST /api/v1/users/{username}/block", a.block)
	route("DELETE /api/v1/users/{username}/block", a.unblock)
	route("POST /api/v1/followers/{username}/accept", a.acceptFollower)
	route("DELETE /api/v1/followers/{username}", a.removeFollower)
	route("GET /api/v1/people", a.people)

	route("GET /api/v1/feed", a.feed)
	route("GET /api/v1/notifications", a.notifications)
	route("POST /api/v1/notifications/read", a.markRead)

	route("GET /api/v1/games/{id}/social", a.gameSocial)
	route("GET /api/v1/games/{id}/lists", a.gameLists)

	route("POST /api/v1/reviews", a.writeReview)
	route("GET /api/v1/reviews/{id}", a.getReview)
	route("PATCH /api/v1/reviews/{id}", a.editReview)
	route("DELETE /api/v1/reviews/{id}", a.deleteReview)
	route("POST /api/v1/reviews/{id}/like", a.likeReview)
	route("DELETE /api/v1/reviews/{id}/like", a.unlikeReview)
	route("POST /api/v1/reports", a.report)

	route("GET /api/v1/lists", a.lists)
	route("POST /api/v1/lists", a.createList)
	route("GET /api/v1/lists/{id}", a.getList)
	route("PATCH /api/v1/lists/{id}", a.editList)
	route("DELETE /api/v1/lists/{id}", a.deleteList)
	route("PUT /api/v1/lists/{id}/items/{game}", a.addToList)
	route("DELETE /api/v1/lists/{id}/items/{game}", a.removeFromList)
	route("POST /api/v1/lists/{id}/order", a.reorderList)

	route("GET /api/v1/invites", a.myInvites)
	route("POST /api/v1/invites", a.createInvite)
	route("DELETE /api/v1/invites/{id}", a.revokeInvite)

	admin := func(pattern string, h http.HandlerFunc) { route(pattern, a.adminOnly(h)) }
	admin("GET /api/v1/admin/users", a.adminUsers)
	admin("PATCH /api/v1/admin/users/{id}", a.adminEditUser)
	admin("POST /api/v1/admin/users/{id}/reset-link", a.adminResetLink)
	admin("GET /api/v1/admin/invites", a.adminInvites)
	admin("GET /api/v1/admin/settings", a.adminSettings)
	admin("PATCH /api/v1/admin/settings", a.adminEditSettings)
	admin("GET /api/v1/admin/reports", a.adminReports)
	admin("POST /api/v1/admin/reports/{id}/resolve", a.adminResolveReport)
	admin("POST /api/v1/admin/hide", a.adminHide)
	admin("GET /api/v1/admin/audit", a.adminAudit)
	admin("GET /api/v1/admin/system", a.adminSystem)
	admin("POST /api/v1/admin/jobs/{name}/run", a.adminRunJob)
	admin("POST /api/v1/admin/games/merge", a.adminMergeGame)
}

func (a *API) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.Social.IsAdmin(r.Context(), user(r)) {
			writeError(w, http.StatusForbidden, "forbidden", "Admins only", nil)
			return
		}
		h(w, r)
	}
}

// socialFail maps social errors; anything else goes to fail.
func socialFail(w http.ResponseWriter, r *http.Request, err error) {
	var se *social.Error
	switch {
	case errors.As(err, &se):
		var fields map[string]string
		if se.Field != "" {
			fields = map[string]string{se.Field: se.Message}
		}
		writeError(w, http.StatusBadRequest, se.Code, se.Message, fields)
	case errors.Is(err, social.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found", nil)
	case errors.Is(err, social.ErrBlocked):
		writeError(w, http.StatusForbidden, "blocked", "You can't do that with this person", nil)
	case errors.Is(err, social.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Not allowed", nil)
	case errors.Is(err, social.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", "The server needs at least one admin", nil)
	default:
		fail(w, r, err)
	}
}

func (a *API) target(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := a.Social.UserByName(r.Context(), r.PathValue("username"))
	if err != nil {
		socialFail(w, r, err)
		return "", false
	}
	return id, true
}

func int64Param(r *http.Request, key string) int64 {
	v, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return v
}

// --- profile ---

func (a *API) myProfile(w http.ResponseWriter, r *http.Request) {
	p, err := a.Social.MyProfile(r.Context(), user(r))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (a *API) editProfile(w http.ResponseWriter, r *http.Request) {
	var in social.ProfileEdit
	if !decode(w, r, &in) {
		return
	}
	p, err := a.Social.UpdateProfile(r.Context(), user(r), in)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (a *API) setAvatar(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Avatar string `json:"avatar"`
	}
	if !decode(w, r, &in) {
		return
	}
	img, ok := decodeDataURL(in.Avatar)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_image", "The avatar must be an image", map[string]string{"avatar": "invalid"})
		return
	}
	if _, err := a.Social.SetAvatar(r.Context(), user(r), img); err != nil {
		socialFail(w, r, err)
		return
	}
	a.myProfile(w, r)
}

func (a *API) clearAvatar(w http.ResponseWriter, r *http.Request) {
	if err := a.Social.ClearAvatar(r.Context(), user(r)); err != nil {
		socialFail(w, r, err)
		return
	}
	a.myProfile(w, r)
}

func (a *API) searchPeople(w http.ResponseWriter, r *http.Request) {
	ps, err := a.Social.SearchPeople(r.Context(), user(r), r.URL.Query().Get("q"), queryInt(r, "limit", 20))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (a *API) profile(w http.ResponseWriter, r *http.Request) {
	p, err := a.Social.Profile(r.Context(), user(r), r.PathValue("username"))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *API) follow(w http.ResponseWriter, r *http.Request) {
	id, ok := a.target(w, r)
	if !ok {
		return
	}
	st, err := a.Social.Follow(r.Context(), user(r), id)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"following": st})
}

func (a *API) unfollow(w http.ResponseWriter, r *http.Request) {
	a.relate(w, r, a.Social.Unfollow)
}

func (a *API) block(w http.ResponseWriter, r *http.Request) { a.relate(w, r, a.Social.Block) }

func (a *API) unblock(w http.ResponseWriter, r *http.Request) { a.relate(w, r, a.Social.Unblock) }

func (a *API) acceptFollower(w http.ResponseWriter, r *http.Request) {
	a.relate(w, r, a.Social.Accept)
}

func (a *API) removeFollower(w http.ResponseWriter, r *http.Request) {
	a.relate(w, r, a.Social.RemoveFollower)
}

func (a *API) relate(w http.ResponseWriter, r *http.Request, fn func(context.Context, string, string) error) {
	id, ok := a.target(w, r)
	if !ok {
		return
	}
	if err := fn(r.Context(), user(r), id); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) people(w http.ResponseWriter, r *http.Request) {
	p, err := a.Social.People(r.Context(), user(r))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- feed and inbox ---

func (a *API) feed(w http.ResponseWriter, r *http.Request) {
	items, next, err := a.Social.Feed(r.Context(), user(r), int64Param(r, "before"), queryInt(r, "limit", 20))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	resp := map[string]any{"items": items}
	if next > 0 {
		resp["next_cursor"] = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) notifications(w http.ResponseWriter, r *http.Request) {
	items, unread, err := a.Social.Inbox(r.Context(), user(r), int64Param(r, "before"), queryInt(r, "limit", 50))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unread": unread})
}

func (a *API) markRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UpTo int64 `json:"up_to"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Social.MarkRead(r.Context(), user(r), in.UpTo); err != nil {
		socialFail(w, r, err)
		return
	}
	unread, _ := a.Social.Unread(r.Context(), user(r))
	writeJSON(w, http.StatusOK, map[string]any{"unread": unread})
}

// --- game page ---

func (a *API) gameSocial(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	gs, err := a.Social.GameSocial(r.Context(), user(r), id)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

func (a *API) gameLists(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ls, err := a.Social.ListsWithGame(r.Context(), user(r), id)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ls})
}

// --- reviews ---

func (a *API) writeReview(w http.ResponseWriter, r *http.Request) {
	var in social.ReviewInput
	if !decode(w, r, &in) {
		return
	}
	rv, err := a.Social.WriteReview(r.Context(), user(r), in)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"review": rv})
}

func (a *API) getReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rv, err := a.Social.Review(r.Context(), user(r), id)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": rv})
}

func (a *API) editReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in social.ReviewInput
	if !decode(w, r, &in) {
		return
	}
	rv, err := a.Social.EditReview(r.Context(), user(r), id, in)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": rv})
}

func (a *API) deleteReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Social.DeleteReview(r.Context(), user(r), id); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) likeReview(w http.ResponseWriter, r *http.Request)   { a.like(w, r, true) }
func (a *API) unlikeReview(w http.ResponseWriter, r *http.Request) { a.like(w, r, false) }

func (a *API) like(w http.ResponseWriter, r *http.Request, like bool) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rv, err := a.Social.Like(r.Context(), user(r), id, like)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": rv})
}

func (a *API) report(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	// You can only report what you can see.
	switch in.Kind {
	case "review":
		id, _ := strconv.ParseInt(in.Target, 10, 64)
		if _, err := a.Social.Review(r.Context(), user(r), id); err != nil {
			socialFail(w, r, err)
			return
		}
	case "list":
		id, _ := strconv.ParseInt(in.Target, 10, 64)
		if _, err := a.Social.List(r.Context(), user(r), id); err != nil {
			socialFail(w, r, err)
			return
		}
	case "user":
		id, err := a.Social.UserByName(r.Context(), in.Target)
		if err != nil {
			socialFail(w, r, err)
			return
		}
		in.Target = id
	}
	if err := a.Social.Report(r.Context(), user(r), in.Kind, in.Target, in.Reason); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// --- lists ---

func (a *API) lists(w http.ResponseWriter, r *http.Request) {
	owner := user(r)
	if name := r.URL.Query().Get("user"); name != "" {
		id, err := a.Social.UserByName(r.Context(), name)
		if err != nil {
			socialFail(w, r, err)
			return
		}
		owner = id
	}
	ls, err := a.Social.ListsOf(r.Context(), user(r), owner)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ls})
}

func (a *API) createList(w http.ResponseWriter, r *http.Request) {
	var in social.ListInput
	if !decode(w, r, &in) {
		return
	}
	l, err := a.Social.CreateList(r.Context(), user(r), in)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"list": l})
}

func (a *API) getList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := a.Social.List(r.Context(), user(r), id)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": l})
}

func (a *API) editList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in social.ListInput
	if !decode(w, r, &in) {
		return
	}
	l, err := a.Social.EditList(r.Context(), user(r), id, in)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": l})
}

func (a *API) deleteList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Social.DeleteList(r.Context(), user(r), id); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func listAndGame(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return 0, 0, false
	}
	game, err := strconv.ParseInt(r.PathValue("game"), 10, 64)
	if err != nil || game == 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid game ID", map[string]string{"game": "must be a number"})
		return 0, 0, false
	}
	return id, game, true
}

func (a *API) addToList(w http.ResponseWriter, r *http.Request) {
	id, game, ok := listAndGame(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	l, err := a.Social.AddToList(r.Context(), user(r), id, game, in.Note)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": l})
}

func (a *API) removeFromList(w http.ResponseWriter, r *http.Request) {
	id, game, ok := listAndGame(w, r)
	if !ok {
		return
	}
	l, err := a.Social.RemoveFromList(r.Context(), user(r), id, game)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": l})
}

func (a *API) reorderList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		GameIDs []int64 `json:"game_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	l, err := a.Social.Reorder(r.Context(), user(r), id, in.GameIDs)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": l})
}

// --- invites ---

func (a *API) myInvites(w http.ResponseWriter, r *http.Request) {
	is, err := a.Social.MyInvites(r.Context(), user(r))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": is, "signup": a.Social.EffectiveSignup(r.Context(), a.Cfg.SignupMode()),
		"can_invite": a.Social.EffectiveSignup(r.Context(), a.Cfg.SignupMode()) != "closed" || a.Social.IsAdmin(r.Context(), user(r))})
}

func (a *API) createInvite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	inv, err := a.Social.CreateInvite(r.Context(), user(r), a.Cfg.SignupMode(), in.Note)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": inv, "path": "/login?invite=" + inv.Token})
}

func (a *API) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := a.Social.RevokeInvite(r.Context(), user(r), r.PathValue("id")); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- admin ---

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	us, err := a.Social.AdminUsers(r.Context())
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": us})
}

func (a *API) adminEditUser(w http.ResponseWriter, r *http.Request) {
	var in social.AdminUserEdit
	if !decode(w, r, &in) {
		return
	}
	if err := a.Social.AdminEditUser(r.Context(), user(r), r.PathValue("id"), in); err != nil {
		socialFail(w, r, err)
		return
	}
	a.adminUsers(w, r)
}

func (a *API) adminResetLink(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("id")
	tok, err := a.Social.CreateResetLink(r.Context(), user(r), target)
	if err != nil {
		socialFail(w, r, err)
		return
	}
	a.Social.Audit(r.Context(), user(r), "user.reset_link", target, "")
	writeJSON(w, http.StatusCreated, map[string]any{"path": "/login?reset=" + tok, "expires_in_hours": int(social.ResetTTL.Hours())})
}

func (a *API) adminInvites(w http.ResponseWriter, r *http.Request) {
	is, err := a.Social.AllInvites(r.Context())
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": is})
}

func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"signup":          a.Social.EffectiveSignup(r.Context(), a.Cfg.SignupMode()),
		"signup_env":      a.Cfg.SignupMode(),
		"signup_override": a.Social.SignupOverride(r.Context()),
	})
}

func (a *API) adminEditSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Signup *string `json:"signup"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Signup != nil {
		if err := a.Social.SetSignupMode(r.Context(), user(r), *in.Signup); err != nil {
			socialFail(w, r, err)
			return
		}
	}
	a.adminSettings(w, r)
}

func (a *API) adminReports(w http.ResponseWriter, r *http.Request) {
	rs, err := a.Social.Reports(r.Context(), user(r), r.URL.Query().Get("all") == "1")
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rs})
}

func (a *API) adminResolveReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Hide bool `json:"hide"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if err := a.Social.ResolveReport(r.Context(), user(r), id, in.Hide); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminHide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		ID     int64  `json:"id"`
		Hidden bool   `json:"hidden"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Social.SetHidden(r.Context(), user(r), in.Kind, strconv.FormatInt(in.ID, 10), in.Hidden); err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminAudit(w http.ResponseWriter, r *http.Request) {
	es, err := a.Social.AuditLog(r.Context(), queryInt(r, "limit", 200))
	if err != nil {
		socialFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": es})
}

func (a *API) adminSystem(w http.ResponseWriter, r *http.Request) {
	if a.System == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, a.System(r.Context()))
}

func (a *API) adminRunJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a.RunJob == nil || !a.RunJob(name) {
		writeError(w, http.StatusNotFound, "not_found", "No such job", nil)
		return
	}
	a.Social.Audit(r.Context(), user(r), "job.run", name, "")
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) adminMergeGame(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomID int64 `json:"custom_id"`
		IntoID   int64 `json:"into_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Games.MergeCustom(r.Context(), in.CustomID, in.IntoID); err != nil {
		writeError(w, http.StatusBadRequest, "merge_failed", err.Error(), nil)
		return
	}
	a.Social.Audit(r.Context(), user(r), "game.merge", strconv.FormatInt(in.CustomID, 10), `{"into":`+strconv.FormatInt(in.IntoID, 10)+`}`)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
