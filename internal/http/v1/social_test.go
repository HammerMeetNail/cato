package v1

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
)

// A tiny valid PNG (1×1).
var onePixelPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestSocialEndpoints(t *testing.T) {
	f := newFixture(t)
	me := f.user(t, "me")
	pal := f.user(t, "pal")
	f.db.Exec(`UPDATE users SET role = 'admin' WHERE id = 'me'`)

	// Profile edits are validated.
	me.json("PATCH", "/api/v1/me/profile", map[string]any{"username": "Not Valid!"}, http.StatusBadRequest)
	got := me.json("PATCH", "/api/v1/me/profile", map[string]any{"username": "Dave", "bio": "Hi", "visibility": "instance", "favourites": []int{1, 3}}, http.StatusOK)
	prof := got["profile"].(map[string]any)
	if prof["username"] != "dave" || len(prof["favourites"].([]any)) != 2 {
		t.Fatalf("profile: %v", prof)
	}
	pal.json("PATCH", "/api/v1/me/profile", map[string]any{"username": "dave"}, http.StatusBadRequest)

	// Avatar upload is re-encoded and served.
	got = me.json("PUT", "/api/v1/me/avatar", map[string]any{"avatar": "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG)}, http.StatusOK)
	if av := got["profile"].(map[string]any)["avatar"].(string); len(av) < 10 {
		t.Fatalf("avatar: %v", av)
	}
	me.json("PUT", "/api/v1/me/avatar", map[string]any{"avatar": "data:image/png;base64,AAAA"}, http.StatusBadRequest)

	// Follow (auto-accepted: instance-visible), profile, feed.
	if got := pal.json("POST", "/api/v1/users/dave/follow", nil, http.StatusOK); got["following"] != "accepted" {
		t.Fatalf("follow: %v", got)
	}
	me.json("PUT", "/api/v1/library/1", map[string]any{"status": "playing"}, http.StatusCreated)
	feed := pal.json("GET", "/api/v1/feed", nil, http.StatusOK)
	if items := feed["items"].([]any); len(items) != 1 || items[0].(map[string]any)["kind"] != "started" {
		t.Fatalf("feed: %v", feed)
	}
	p := pal.json("GET", "/api/v1/users/dave", nil, http.StatusOK)
	if p["stats"] == nil || len(p["playing"].([]any)) != 1 {
		t.Fatalf("profile view: %v", p)
	}
	pal.json("GET", "/api/v1/users/nobody", nil, http.StatusNotFound)

	// Reviews: write, like (notifies), game social.
	rv := me.json("POST", "/api/v1/reviews", map[string]any{"game_id": 1, "body": "Brilliant", "spoiler": false}, http.StatusCreated)["review"].(map[string]any)
	id := int(rv["id"].(float64))
	pal.json("POST", fmt.Sprintf("/api/v1/reviews/%d/like", id), nil, http.StatusOK)
	inbox := me.json("GET", "/api/v1/notifications", nil, http.StatusOK)
	if inbox["unread"].(float64) != 2 { // follow + like
		t.Fatalf("inbox: %v", inbox)
	}
	me.json("POST", "/api/v1/notifications/read", map[string]any{}, http.StatusOK)
	gs := pal.json("GET", "/api/v1/games/1/social", nil, http.StatusOK)
	if len(gs["reviews"].([]any)) != 1 || len(gs["friends"].([]any)) != 1 {
		t.Fatalf("game social: %v", gs)
	}
	pal.json("PATCH", fmt.Sprintf("/api/v1/reviews/%d", id), map[string]any{"body": "mine now"}, http.StatusNotFound)

	// Lists.
	l := me.json("POST", "/api/v1/lists", map[string]any{"title": "Comfort games", "ranked": true}, http.StatusCreated)["list"].(map[string]any)
	lid := int(l["id"].(float64))
	me.json("PUT", fmt.Sprintf("/api/v1/lists/%d/items/1", lid), map[string]any{"note": "always"}, http.StatusOK)
	me.json("PUT", fmt.Sprintf("/api/v1/lists/%d/items/3", lid), nil, http.StatusOK)
	got = me.json("POST", fmt.Sprintf("/api/v1/lists/%d/order", lid), map[string]any{"game_ids": []int{3, 1}}, http.StatusOK)
	if first := got["list"].(map[string]any)["items"].([]any)[0].(map[string]any)["game"].(map[string]any)["id"]; first != float64(3) {
		t.Fatalf("order: %v", got)
	}
	pal.json("PUT", fmt.Sprintf("/api/v1/lists/%d/items/2", lid), nil, http.StatusNotFound)
	if ls := pal.json("GET", "/api/v1/lists?user=dave", nil, http.StatusOK)["items"].([]any); len(ls) != 1 {
		t.Fatalf("pal sees the list: %v", ls)
	}

	// Reports reach admins; non-admins can't use admin endpoints.
	pal.json("POST", "/api/v1/reports", map[string]any{"kind": "review", "target": fmt.Sprint(id), "reason": "spoilers"}, http.StatusCreated)
	pal.json("GET", "/api/v1/admin/reports", nil, http.StatusForbidden)
	pal.json("GET", "/api/v1/admin/users", nil, http.StatusForbidden)
	reps := me.json("GET", "/api/v1/admin/reports", nil, http.StatusOK)["items"].([]any)
	if len(reps) != 1 {
		t.Fatalf("reports: %v", reps)
	}
	rid := int(reps[0].(map[string]any)["id"].(float64))
	me.json("POST", fmt.Sprintf("/api/v1/admin/reports/%d/resolve", rid), map[string]any{"hide": true}, http.StatusOK)
	if gs := pal.json("GET", "/api/v1/games/1/social", nil, http.StatusOK); len(gs["reviews"].([]any)) != 0 {
		t.Fatalf("hidden review still listed: %v", gs["reviews"])
	}
	me.json("PATCH", "/api/v1/admin/users/me", map[string]any{"role": "user"}, http.StatusConflict)
	me.json("PATCH", "/api/v1/admin/settings", map[string]any{"signup": "invite"}, http.StatusOK)
	if audit := me.json("GET", "/api/v1/admin/audit", nil, http.StatusOK)["items"].([]any); len(audit) < 2 {
		t.Fatalf("audit: %v", audit)
	}
	link := me.json("POST", "/api/v1/admin/users/pal/reset-link", nil, http.StatusCreated)
	if p, _ := link["path"].(string); len(p) < 20 {
		t.Fatalf("reset link: %v", link)
	}

	// Invites.
	inv := pal.json("POST", "/api/v1/invites", map[string]any{"note": "for Sam"}, http.StatusCreated)
	if p, _ := inv["path"].(string); len(p) < 20 {
		t.Fatalf("invite: %v", inv)
	}
	if is := pal.json("GET", "/api/v1/invites", nil, http.StatusOK)["items"].([]any); len(is) != 1 {
		t.Fatalf("invites: %v", is)
	}

	// Blocks hide the profile from the blocked person.
	me.json("POST", "/api/v1/users/pal/block", nil, http.StatusOK)
	pal.json("GET", "/api/v1/users/dave", nil, http.StatusNotFound)
	pal.json("POST", "/api/v1/users/dave/follow", nil, http.StatusForbidden)
}
