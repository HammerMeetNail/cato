package social

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"cato/internal/db"
)

type fixture struct {
	t   *testing.T
	db  *db.DB
	s   *Service
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: database, s: New(database, filepath.Join(t.TempDir(), "avatars")), ctx: context.Background()}
	f.exec(`INSERT INTO games (id, name, slug, normalized_name, cover_url, platforms_json) VALUES
		(1, 'Hades', 'hades', 'hades', '', '[]'), (2, 'Celeste', 'celeste', 'celeste', '', '[]')`)
	return f
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *fixture) user(id, visibility string) {
	f.t.Helper()
	f.exec(`INSERT INTO users (id, email, username, visibility) VALUES (?, ?, ?, ?)`, id, id+"@example.com", id, visibility)
}

func (f *fixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// TestAuthorizationMatrix walks every combination of the owner's profile
// level, the item's own level and how the viewer relates to the owner, and
// checks profile content, reviews, lists, the feed and the game page all
// agree with the rule (plan §7 security work).
func TestAuthorizationMatrix(t *testing.T) {
	relations := []string{"self", "stranger", "pending", "follower", "blocked_by_owner", "blocks_owner", "disabled_owner", "admin_stranger"}
	levels := []string{Private, Followers, Instance}
	itemLevels := []string{Inherit, Private, Followers, Instance}
	for _, ownerLevel := range levels {
		for _, rel := range relations {
			for _, itemLevel := range itemLevels {
				name := fmt.Sprintf("%s/%s/%s", ownerLevel, rel, itemLevel)
				t.Run(name, func(t *testing.T) {
					f := newFixture(t)
					f.user("owner", ownerLevel)
					viewer := "viewer"
					if rel == "self" {
						viewer = "owner"
					} else {
						f.user("viewer", Private)
					}
					switch rel {
					case "pending":
						f.exec(`INSERT INTO follows (follower_id, followee_id, status) VALUES ('viewer', 'owner', 'pending')`)
					case "follower":
						f.exec(`INSERT INTO follows (follower_id, followee_id, status) VALUES ('viewer', 'owner', 'accepted')`)
					case "blocked_by_owner":
						f.exec(`INSERT INTO follows (follower_id, followee_id, status) VALUES ('viewer', 'owner', 'accepted')`)
						f.exec(`INSERT INTO blocks (blocker_id, blocked_id) VALUES ('owner', 'viewer')`)
					case "blocks_owner":
						f.exec(`INSERT INTO follows (follower_id, followee_id, status) VALUES ('viewer', 'owner', 'accepted')`)
						f.exec(`INSERT INTO blocks (blocker_id, blocked_id) VALUES ('viewer', 'owner')`)
					case "admin_stranger":
						f.exec(`UPDATE users SET role = 'admin' WHERE id = 'viewer'`)
					}
					f.exec(`INSERT INTO library_items (user_id, game_id, status, rating) VALUES ('owner', 1, 'completed', 80)`)
					rv, err := f.s.WriteReview(f.ctx, "owner", ReviewInput{GameID: 1, Body: "Great", Visibility: itemLevel})
					if err != nil {
						t.Fatal(err)
					}
					vis := itemLevel
					l, err := f.s.CreateList(f.ctx, "owner", ListInput{Title: ptr("Faves"), Visibility: &vis})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := f.s.AddToList(f.ctx, "owner", l.ID, 1, ""); err != nil {
						t.Fatal(err)
					}
					if rel == "disabled_owner" {
						f.exec(`UPDATE users SET disabled = 1 WHERE id = 'owner'`)
					}

					// The rule, written out independently of the SQL.
					want := func(level string) bool {
						if rel == "self" {
							return true
						}
						switch rel {
						case "blocked_by_owner", "blocks_owner", "disabled_owner":
							return false
						}
						if level == Inherit {
							level = ownerLevel
						}
						return level == Instance || (level == Followers && rel == "follower")
					}
					profileWant := want(Inherit)
					itemWant := want(itemLevel)

					// Profile content.
					p, err := f.s.Profile(f.ctx, viewer, "owner")
					switch {
					case rel == "blocked_by_owner" || rel == "disabled_owner":
						if !errors.Is(err, ErrNotFound) {
							t.Fatalf("profile: want not found, got %v", err)
						}
					case err != nil:
						t.Fatal(err)
					default:
						if got := p.Relationship.CanSee && !p.Relationship.Blocked; got != profileWant {
							t.Errorf("profile can_see = %v, want %v", got, profileWant)
						}
						if p.Stats != nil != profileWant {
							t.Errorf("profile stats shown = %v, want %v", p.Stats != nil, profileWant)
						}
						if profileWant {
							if got := len(p.Reviews) == 1; got != itemWant {
								t.Errorf("profile reviews visible = %v, want %v", got, itemWant)
							}
							if got := len(p.Lists) == 1; got != itemWant {
								t.Errorf("profile lists visible = %v, want %v", got, itemWant)
							}
						}
					}

					// Direct reads.
					_, err = f.s.Review(f.ctx, viewer, rv.ID)
					if got := err == nil; got != itemWant {
						t.Errorf("review visible = %v (%v), want %v", got, err, itemWant)
					}
					_, err = f.s.List(f.ctx, viewer, l.ID)
					if got := err == nil; got != itemWant {
						t.Errorf("list visible = %v (%v), want %v", got, err, itemWant)
					}
					ok, err := f.s.CanSee(f.ctx, viewer, "owner", itemLevel)
					if err != nil || ok != itemWant {
						t.Errorf("CanSee = %v (%v), want %v", ok, err, itemWant)
					}

					// Game page: reviews, and the community rating counts the
					// owner only when the profile is visible.
					gs, err := f.s.GameSocial(f.ctx, viewer, 1)
					if err != nil {
						t.Fatal(err)
					}
					if got := len(gs.Reviews) == 1; got != itemWant {
						t.Errorf("game reviews visible = %v, want %v", got, itemWant)
					}
					if got := gs.Community.Ratings == 1; got != profileWant {
						t.Errorf("community counts owner = %v, want %v", got, profileWant)
					}
					friendWant := rel == "follower" && ownerLevel != Private
					if got := len(gs.Friends) == 1; got != friendWant {
						t.Errorf("friends row = %v, want %v", got, friendWant)
					}

					// Feed: only people you follow, at their profile level, with
					// reviewed/listed items at their own level.
					if rel != "self" {
						feed, _, err := f.s.Feed(f.ctx, viewer, 0, 50)
						if err != nil {
							t.Fatal(err)
						}
						kinds := map[string]bool{}
						for _, e := range feed {
							kinds[e.Kind] = true
						}
						followerSees := rel == "follower" && profileWant
						if kinds["reviewed"] != (followerSees && itemWant) {
							t.Errorf("feed reviewed = %v, want %v", kinds["reviewed"], followerSees && itemWant)
						}
						if kinds["listed"] != (followerSees && itemWant) {
							t.Errorf("feed listed = %v, want %v", kinds["listed"], followerSees && itemWant)
						}
					}

					// Likes need visibility too.
					if rel != "self" {
						_, err := f.s.Like(f.ctx, viewer, rv.ID, true)
						if got := err == nil; got != itemWant {
							t.Errorf("like allowed = %v (%v), want %v", got, err, itemWant)
						}
					}
				})
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestFollowRequestsAndBlocks(t *testing.T) {
	f := newFixture(t)
	f.user("a", Private)
	f.user("b", Private)
	f.user("c", Instance)
	if st, err := f.s.Follow(f.ctx, "a", "b"); err != nil || st != "pending" {
		t.Fatalf("follow private: %q %v", st, err)
	}
	if st, _ := f.s.Follow(f.ctx, "a", "c"); st != "accepted" {
		t.Fatalf("follow instance-visible should auto-accept, got %q", st)
	}
	if n := f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = 'b' AND kind = 'follow_request'`); n != 1 {
		t.Fatalf("request notification: %d", n)
	}
	if err := f.s.Accept(f.ctx, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = 'a' AND kind = 'follow_accepted'`); n != 1 {
		t.Fatalf("accepted notification: %d", n)
	}
	if err := f.s.Accept(f.ctx, "b", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("accepting twice: %v", err)
	}
	// Going instance-wide accepts what's waiting.
	f.s.Follow(f.ctx, "c", "b")
	f.s.UpdateProfile(f.ctx, "b", ProfileEdit{Visibility: ptr(Instance)})
	if n := f.count(`SELECT COUNT(*) FROM follows WHERE followee_id = 'b' AND status = 'accepted'`); n != 2 {
		t.Fatalf("instance-wide should accept pending requests: %d", n)
	}
	// Blocking ends follows both ways and stops new ones.
	if err := f.s.Block(f.ctx, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM follows WHERE (follower_id = 'a' AND followee_id = 'b') OR (follower_id = 'b' AND followee_id = 'a')`); n != 0 {
		t.Fatalf("block should end follows: %d", n)
	}
	if _, err := f.s.Follow(f.ctx, "a", "b"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("follow after block: %v", err)
	}
	if ps, _ := f.s.SearchPeople(f.ctx, "a", "b", 10); len(ps) != 0 {
		t.Fatalf("blocked people shouldn't turn up in search: %+v", ps)
	}
	// Private people only turn up for their exact username.
	f.user("bob", Private)
	f.user("bobby", Instance)
	ps, _ := f.s.SearchPeople(f.ctx, "c", "bo", 10)
	if len(ps) != 1 || ps[0].Username != "bobby" {
		t.Fatalf("prefix search: %+v", ps)
	}
	ps, _ = f.s.SearchPeople(f.ctx, "c", "bob", 10)
	if len(ps) != 2 || ps[0].Username != "bob" {
		t.Fatalf("exact search: %+v", ps)
	}
}

func TestInvitesAndSignupModes(t *testing.T) {
	f := newFixture(t)
	// The first account is always allowed, and is the admin.
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "admin", Email: "Dave.O+cato@example.com"}); err != nil {
		t.Fatal(err)
	}
	var role, username string
	f.db.QueryRow(`SELECT role, username FROM users WHERE id = 'admin'`).Scan(&role, &username)
	if role != "admin" || username != "dave_o_cato" {
		t.Fatalf("first user: role %q username %q", role, username)
	}
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "x", Email: "x@example.com"}); !errors.Is(err, ErrSignupClosed) {
		t.Fatalf("closed signup: %v", err)
	}
	// In closed mode an admin's invite works.
	inv, err := f.s.CreateInvite(f.ctx, "admin", "closed", "for Sam")
	if err != nil {
		t.Fatal(err)
	}
	if info := f.s.CheckInvite(f.ctx, inv.Token, "closed"); !info.Valid {
		t.Fatalf("invite should be valid: %+v", info)
	}
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "sam", Email: "sam@example.com", Invite: inv.Token}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM follows WHERE status = 'accepted' AND ((follower_id = 'sam' AND followee_id = 'admin') OR (follower_id = 'admin' AND followee_id = 'sam'))`); n != 2 {
		t.Fatalf("invite should make a mutual follow: %d", n)
	}
	// Single use.
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "y", Email: "y@example.com", Invite: inv.Token}); !errors.Is(err, ErrSignupClosed) {
		t.Fatalf("reused invite: %v", err)
	}
	// A user can't invite in closed mode; can in invite mode (admin override).
	if _, err := f.s.CreateInvite(f.ctx, "sam", "closed", ""); err == nil {
		t.Fatal("user invite in closed mode should fail")
	}
	if err := f.s.SetSignupMode(f.ctx, "admin", "invite"); err != nil {
		t.Fatal(err)
	}
	inv2, err := f.s.CreateInvite(f.ctx, "sam", "closed", "")
	if err != nil {
		t.Fatal(err)
	}
	// Expired invites fail.
	f.exec(`UPDATE invites SET expires_at = '2000-01-01T00:00:00Z' WHERE token_hash = ?`, hashToken(inv2.Token))
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "z", Email: "z@example.com", Invite: inv2.Token}); !errors.Is(err, ErrSignupClosed) {
		t.Fatalf("expired invite: %v", err)
	}
	// Revoked invites fail.
	inv3, _ := f.s.CreateInvite(f.ctx, "sam", "closed", "")
	if err := f.s.RevokeInvite(f.ctx, "sam", inv3.ID); err != nil {
		t.Fatal(err)
	}
	if f.s.CheckInvite(f.ctx, inv3.Token, "closed").Valid {
		t.Fatal("revoked invite should be invalid")
	}
	// Duplicate usernames get a suffix.
	f.s.SetSignupMode(f.ctx, "admin", "open")
	if err := f.s.CreateUser(f.ctx, "closed", NewUser{ID: "sam2", Email: "sam@elsewhere.test"}); err != nil {
		t.Fatal(err)
	}
	f.db.QueryRow(`SELECT username FROM users WHERE id = 'sam2'`).Scan(&username)
	if username != "sam_2" {
		t.Fatalf("duplicate username: %q", username)
	}
	// An audit entry exists for each mode change.
	if n := f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'settings.signup'`); n != 2 {
		t.Fatalf("audit entries: %d", n)
	}
}

func TestResetLinkAndLastAdmin(t *testing.T) {
	f := newFixture(t)
	f.user("admin", Private)
	f.exec(`UPDATE users SET role = 'admin', password_hash = 'old' WHERE id = 'admin'`)
	f.user("u", Private)
	f.exec(`INSERT INTO sessions (id, user_id, csrf_token, expires_at) VALUES ('s1', 'u', 'c', '2999-01-01')`)
	tok, err := f.s.CreateResetLink(f.ctx, "admin", "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UseResetToken(f.ctx, tok, "newhash"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM sessions WHERE user_id = 'u'`); n != 0 {
		t.Fatal("reset should sign out everywhere")
	}
	if _, err := f.s.UseResetToken(f.ctx, tok, "again"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("reset token reuse: %v", err)
	}
	if err := f.s.AdminEditUser(f.ctx, "admin", "admin", AdminUserEdit{Role: ptr("user")}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the last admin: %v", err)
	}
	if err := f.s.AdminEditUser(f.ctx, "admin", "u", AdminUserEdit{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Person(f.ctx, "u"); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled people are hidden")
	}
}

func TestReviewsListsFeedAndModeration(t *testing.T) {
	f := newFixture(t)
	f.user("me", Instance)
	f.user("friend", Instance)
	f.user("other", Instance)
	f.s.Follow(f.ctx, "me", "friend")
	f.exec(`INSERT INTO library_items (user_id, game_id, status, rating) VALUES ('friend', 1, 'completed', 90), ('other', 1, 'completed', 50)`)
	other, _ := f.s.WriteReview(f.ctx, "other", ReviewInput{GameID: 1, Body: "Fine"})
	f.s.Like(f.ctx, "friend", other.ID, true)
	fr, _ := f.s.WriteReview(f.ctx, "friend", ReviewInput{GameID: 1, Body: "Loved it", Spoiler: true})
	rs, err := f.s.GameReviews(f.ctx, "me", 1, 10)
	if err != nil || len(rs) != 2 || rs[0].ID != fr.ID || !rs[0].Friend {
		t.Fatalf("friends' reviews come first: %+v %v", rs, err)
	}
	if n := f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = 'other' AND kind = 'like'`); n != 1 {
		t.Fatalf("like notification: %d", n)
	}
	gs, _ := f.s.GameSocial(f.ctx, "me", 1)
	if gs.Community.Ratings != 2 || gs.Community.Average != 70 || gs.Community.Histogram[8] != 1 || gs.Community.Histogram[4] != 1 {
		t.Fatalf("community: %+v", gs.Community)
	}
	if len(gs.Friends) != 1 || gs.Friends[0].ReviewID != fr.ID {
		t.Fatalf("friends: %+v", gs.Friends)
	}
	if _, err := f.s.WriteReview(f.ctx, "me", ReviewInput{GameID: 1, Body: string(make([]rune, MaxReview+1))}); err == nil {
		t.Fatal("over-long review accepted")
	}

	// Report and hide.
	f.s.Report(f.ctx, "me", "review", fmt.Sprint(other.ID), "spam")
	f.user("admin", Private)
	f.exec(`UPDATE users SET role = 'admin' WHERE id = 'admin'`)
	reps, _ := f.s.Reports(f.ctx, "admin", false)
	if len(reps) != 1 || reps[0].Excerpt != "Fine" {
		t.Fatalf("reports: %+v", reps)
	}
	if err := f.s.ResolveReport(f.ctx, "admin", reps[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if rs, _ := f.s.GameReviews(f.ctx, "me", 1, 10); len(rs) != 1 {
		t.Fatalf("hidden review still shown: %d", len(rs))
	}
	if r, err := f.s.Review(f.ctx, "other", other.ID); err != nil || !r.Hidden {
		t.Fatalf("author still sees their hidden review: %v", err)
	}

	// Lists: ranked order and reorder.
	l, _ := f.s.CreateList(f.ctx, "friend", ListInput{Title: ptr("Comfort games"), Ranked: ptr(true)})
	f.s.AddToList(f.ctx, "friend", l.ID, 1, "")
	f.s.AddToList(f.ctx, "friend", l.ID, 2, "so good")
	got, _ := f.s.Reorder(f.ctx, "friend", l.ID, []int64{2})
	if got.Items[0].Game.ID != 2 || got.Items[0].Note != "so good" || got.Items[1].Game.ID != 1 {
		t.Fatalf("reorder: %+v", got.Items)
	}
	if _, err := f.s.AddToList(f.ctx, "me", l.ID, 1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("editing someone else's list: %v", err)
	}

	// Feed groups a day's logging into one line.
	for _, g := range []int64{1, 2, 1} {
		f.exec(`INSERT INTO activity (user_id, kind, game_id, data_json) VALUES ('friend', 'logged', ?, '{"minutes":60}')`, g)
	}
	feed, _, err := f.s.Feed(f.ctx, "me", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if feed[0].Kind != "logged" || feed[0].Minutes != 180 || len(feed[0].Games) != 2 {
		t.Fatalf("grouped feed: %+v", feed[0])
	}
}
