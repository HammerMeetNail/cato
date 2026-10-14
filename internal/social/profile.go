package social

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/covers"
	"cato/internal/library"
)

// Person is how someone appears next to their content.
type Person struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
}

// personCols selects the columns scanPerson reads, from users aliased u.
const personCols = `u.id, COALESCE(u.username, ''), u.display_name, u.avatar_path, u.use_gravatar, u.email, u.avatar_url`

func scanPerson(sc interface{ Scan(...any) error }, extra ...any) (Person, error) {
	var p Person
	var avatarPath, email, avatarURL string
	var gravatar int
	err := sc.Scan(append([]any{&p.ID, &p.Username, &p.DisplayName, &avatarPath, &gravatar, &email, &avatarURL}, extra...)...)
	p.Avatar = avatarFor(avatarPath, gravatar != 0, email)
	_ = avatarURL // Google's picture URL stays unused: no third-party image without opt-in.
	return p, err
}

// avatarFor is an uploaded avatar, the opt-in Gravatar, or "" (the app
// draws initials).
func avatarFor(path string, gravatar bool, email string) string {
	if path != "" {
		return "/avatars/" + path
	}
	if gravatar {
		sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
		return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?s=160&d=identicon"
	}
	return ""
}

// Person loads one account (disabled accounts are not found).
func (s *Service) Person(ctx context.Context, userID string) (Person, error) {
	p, err := scanPerson(s.db.QueryRowContext(ctx, `SELECT `+personCols+` FROM users u WHERE u.id = ? AND u.disabled = 0`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Relationship is how the viewer and a person are connected.
type Relationship struct {
	Self       bool   `json:"self"`
	Following  string `json:"following"` // "", "pending" or "accepted"
	FollowsYou string `json:"follows_you"`
	Blocked    bool   `json:"blocked"`    // you blocked them
	BlocksYou  bool   `json:"blocks_you"` // they blocked you
	CanSee     bool   `json:"can_see"`    // their profile content
	Visibility string `json:"visibility"` // their profile level
}

func (s *Service) relationship(ctx context.Context, viewer, other string) (Relationship, error) {
	r := Relationship{Self: viewer == other}
	if err := s.db.QueryRowContext(ctx, `SELECT visibility FROM users WHERE id = ?`, other).Scan(&r.Visibility); err != nil {
		return r, err
	}
	s.db.QueryRowContext(ctx, `SELECT status FROM follows WHERE follower_id = ? AND followee_id = ?`, viewer, other).Scan(&r.Following)
	s.db.QueryRowContext(ctx, `SELECT status FROM follows WHERE follower_id = ? AND followee_id = ?`, other, viewer).Scan(&r.FollowsYou)
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocks WHERE blocker_id = ? AND blocked_id = ?`, viewer, other).Scan(&n)
	r.Blocked = n > 0
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocks WHERE blocker_id = ? AND blocked_id = ?`, other, viewer).Scan(&n)
	r.BlocksYou = n > 0
	var err error
	r.CanSee, err = s.CanSee(ctx, viewer, other, Inherit)
	return r, err
}

// ProfileGame is a game on a profile.
type ProfileGame struct {
	library.Game
	Status   string `json:"status,omitempty"`
	Rating   int64  `json:"rating,omitempty"`
	Minutes  int64  `json:"minutes,omitempty"`
	Finished string `json:"finished,omitempty"`
}

// Profile is what #u/<username> shows.
type Profile struct {
	Person       Person        `json:"person"`
	Relationship Relationship  `json:"relationship"`
	Bio          string        `json:"bio,omitempty"`
	Favourites   []ProfileGame `json:"favourites"`
	Playing      []ProfileGame `json:"playing"`
	Finished     []ProfileGame `json:"recent_finishes"`
	Stats        *ProfileStats `json:"stats,omitempty"`
	Lists        []ListSummary `json:"lists"`
	Reviews      []Review      `json:"reviews"`
	Followers    int           `json:"followers"`
	Following    int           `json:"following"`
}

type ProfileStats struct {
	Games        int   `json:"games"`
	Finished     int   `json:"finished"`
	Minutes      int64 `json:"minutes"`
	FinishedYear int   `json:"finished_this_year"`
}

// UserByName resolves a username (case-insensitive) to an enabled account.
func (s *Service) UserByName(ctx context.Context, username string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ? COLLATE NOCASE AND disabled = 0`,
		strings.TrimPrefix(username, "@")).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// Profile loads someone's profile as viewer sees it. Anyone signed in sees
// the name and avatar (so they can ask to follow); the rest follows the
// owner's visibility. A block either way hides the profile entirely.
func (s *Service) Profile(ctx context.Context, viewer, username string) (*Profile, error) {
	owner, err := s.UserByName(ctx, username)
	if err != nil {
		return nil, err
	}
	rel, err := s.relationship(ctx, viewer, owner)
	if err != nil {
		return nil, err
	}
	if rel.BlocksYou {
		return nil, ErrNotFound
	}
	p := &Profile{Relationship: rel, Favourites: []ProfileGame{}, Playing: []ProfileGame{}, Finished: []ProfileGame{}, Lists: []ListSummary{}, Reviews: []Review{}}
	if p.Person, err = s.Person(ctx, owner); err != nil {
		return nil, err
	}
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE followee_id = ? AND status = 'accepted'`, owner).Scan(&p.Followers)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE follower_id = ? AND status = 'accepted'`, owner).Scan(&p.Following)
	if !rel.CanSee || rel.Blocked {
		return p, nil
	}
	var favJSON string
	s.db.QueryRowContext(ctx, `SELECT bio, favourites_json FROM users WHERE id = ?`, owner).Scan(&p.Bio, &favJSON)
	var favs []int64
	json.Unmarshal([]byte(favJSON), &favs)
	for _, id := range favs {
		if g, err := s.profileGame(ctx, owner, id); err == nil {
			p.Favourites = append(p.Favourites, g)
		}
	}
	if p.Playing, err = s.profileGames(ctx, owner, `li.status = 'playing'`, `li.updated_at DESC`, 8); err != nil {
		return nil, err
	}
	if p.Finished, err = s.profileGames(ctx, owner, `li.status = 'completed'`, `COALESCE(li.completed_at, li.updated_at) DESC`, 8); err != nil {
		return nil, err
	}
	st := &ProfileStats{}
	year := time.Now().UTC().Format("2006")
	s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status = 'completed'), 0), COALESCE(SUM(playtime_minutes), 0),
			COALESCE(SUM(status = 'completed' AND substr(completed_at, 1, 4) = ?), 0)
		FROM library_items WHERE user_id = ?`, year, owner).Scan(&st.Games, &st.Finished, &st.Minutes, &st.FinishedYear)
	p.Stats = st
	if p.Lists, err = s.ListsOf(ctx, viewer, owner); err != nil {
		return nil, err
	}
	if p.Reviews, err = s.reviews(ctx, viewer, `r.user_id = ?`, []any{owner}, `r.created_at DESC`, 20); err != nil {
		return nil, err
	}
	return p, nil
}

const profileGameCols = `g.id, g.name, g.slug, g.cover_url, COALESCE(g.local_cover_path, ''), COALESCE(g.first_release_date, 0),
	COALESCE(li.status, ''), COALESCE(li.rating, 0), COALESCE(li.playtime_minutes, 0), COALESCE(li.completed_at, '')`

func scanProfileGame(sc interface{ Scan(...any) error }) (ProfileGame, error) {
	var g ProfileGame
	var remote, local string
	err := sc.Scan(&g.ID, &g.Name, &g.Slug, &remote, &local, &g.ReleaseDate, &g.Status, &g.Rating, &g.Minutes, &g.Finished)
	g.Cover = library.CoverURL(local, remote)
	g.Platforms = []string{}
	return g, err
}

func (s *Service) profileGame(ctx context.Context, owner string, gameID int64) (ProfileGame, error) {
	return scanProfileGame(s.db.QueryRowContext(ctx, `SELECT `+profileGameCols+` FROM games g
		LEFT JOIN library_items li ON li.game_id = g.id AND li.user_id = ? WHERE g.id = ?`, owner, gameID))
}

func (s *Service) profileGames(ctx context.Context, owner, where, order string, limit int) ([]ProfileGame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileGameCols+` FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND `+where+` ORDER BY `+order+` LIMIT ?`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileGame{}
	for rows.Next() {
		g, err := scanProfileGame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ProfileEdit changes your own profile; nil fields stay.
type ProfileEdit struct {
	Username    *string  `json:"username"`
	DisplayName *string  `json:"display_name"`
	Bio         *string  `json:"bio"`
	Visibility  *string  `json:"visibility"`
	Favourites  *[]int64 `json:"favourites"`
	UseGravatar *bool    `json:"use_gravatar"`
}

// MyProfile is your own profile settings.
type MyProfile struct {
	Person
	Bio         string        `json:"bio"`
	Visibility  string        `json:"visibility"`
	Favourites  []ProfileGame `json:"favourites"`
	UseGravatar bool          `json:"use_gravatar"`
	HasUpload   bool          `json:"has_avatar_upload"`
	Role        string        `json:"role"`
}

func (s *Service) MyProfile(ctx context.Context, userID string) (*MyProfile, error) {
	var m MyProfile
	var favJSON, avatarPath string
	var gravatar int
	p, err := scanPerson(s.db.QueryRowContext(ctx, `SELECT `+personCols+`, u.bio, u.visibility, u.favourites_json, u.use_gravatar, u.avatar_path, u.role
		FROM users u WHERE u.id = ?`, userID), &m.Bio, &m.Visibility, &favJSON, &gravatar, &avatarPath, &m.Role)
	if err != nil {
		return nil, err
	}
	m.Person, m.UseGravatar, m.HasUpload = p, gravatar != 0, avatarPath != ""
	var favs []int64
	json.Unmarshal([]byte(favJSON), &favs)
	m.Favourites = []ProfileGame{}
	for _, id := range favs {
		if g, err := s.profileGame(ctx, userID, id); err == nil {
			m.Favourites = append(m.Favourites, g)
		}
	}
	return &m, nil
}

func (s *Service) UpdateProfile(ctx context.Context, userID string, e ProfileEdit) (*MyProfile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if e.Username != nil {
		u, err := NormalizeUsername(*e.Username)
		if err != nil {
			return nil, err
		}
		var n int
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE AND id != ?`, u, userID).Scan(&n)
		if n > 0 {
			return nil, invalid("username_taken", "username", "That username is taken")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET username = ? WHERE id = ?`, u, userID); err != nil {
			return nil, err
		}
	}
	if e.DisplayName != nil {
		name := strings.TrimSpace(*e.DisplayName)
		if len([]rune(name)) > 64 {
			return nil, invalid("too_long", "display_name", "Use at most 64 characters")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, name, userID); err != nil {
			return nil, err
		}
	}
	if e.Bio != nil {
		bio := strings.TrimSpace(*e.Bio)
		if len([]rune(bio)) > MaxBio {
			return nil, invalid("too_long", "bio", "Keep your bio under 500 characters")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET bio = ? WHERE id = ?`, bio, userID); err != nil {
			return nil, err
		}
	}
	if e.Visibility != nil {
		if !validLevel(*e.Visibility, false) {
			return nil, invalid("invalid_visibility", "visibility", "Choose private, followers or instance")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET visibility = ? WHERE id = ?`, *e.Visibility, userID); err != nil {
			return nil, err
		}
		// Opening up to the whole instance accepts the waiting requests.
		if *e.Visibility == Instance {
			if _, err := tx.ExecContext(ctx, `UPDATE follows SET status = 'accepted' WHERE followee_id = ? AND status = 'pending'`, userID); err != nil {
				return nil, err
			}
		}
	}
	if e.Favourites != nil {
		favs := []int64{}
		seen := map[int64]bool{}
		for _, id := range *e.Favourites {
			if id != 0 && !seen[id] {
				seen[id] = true
				favs = append(favs, id)
			}
		}
		if len(favs) > MaxFavourites {
			return nil, invalid("too_many", "favourites", "Pick up to four favourites")
		}
		for _, id := range favs {
			var n int
			tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE id = ?`, id).Scan(&n)
			if n == 0 {
				return nil, invalid("game_not_found", "favourites", "Game not found")
			}
		}
		b, _ := json.Marshal(favs)
		if _, err := tx.ExecContext(ctx, `UPDATE users SET favourites_json = ? WHERE id = ?`, string(b), userID); err != nil {
			return nil, err
		}
	}
	if e.UseGravatar != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET use_gravatar = ? WHERE id = ?`, *e.UseGravatar, userID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.MyProfile(ctx, userID)
}

// SetAvatar re-encodes an uploaded image to 256 px JPEG (dropping EXIF and
// anything else in the file) and makes it the user's avatar.
func (s *Service) SetAvatar(ctx context.Context, userID string, data []byte) (string, error) {
	img, err := covers.Reencode(data, 256, 256)
	if err != nil {
		return "", invalid("invalid_image", "avatar", "Use a JPEG, PNG or GIF image")
	}
	if err := os.MkdirAll(s.AvatarDir, 0o755); err != nil {
		return "", err
	}
	// A fresh name per upload: avatars are served immutable.
	name := userID + "-" + auth.RandomToken(6) + ".jpg"
	tmp := filepath.Join(s.AvatarDir, name+".tmp")
	if err := os.WriteFile(tmp, img, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(s.AvatarDir, name)); err != nil {
		os.Remove(tmp)
		return "", err
	}
	var old string
	s.db.QueryRowContext(ctx, `SELECT avatar_path FROM users WHERE id = ?`, userID).Scan(&old)
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET avatar_path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, name, userID); err != nil {
		os.Remove(filepath.Join(s.AvatarDir, name))
		return "", err
	}
	s.removeAvatarFile(old)
	return "/avatars/" + name, nil
}

func (s *Service) ClearAvatar(ctx context.Context, userID string) error {
	var old string
	s.db.QueryRowContext(ctx, `SELECT avatar_path FROM users WHERE id = ?`, userID).Scan(&old)
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET avatar_path = '' WHERE id = ?`, userID); err != nil {
		return err
	}
	s.removeAvatarFile(old)
	return nil
}

func (s *Service) removeAvatarFile(name string) {
	if name != "" && filepath.Base(name) == name {
		os.Remove(filepath.Join(s.AvatarDir, name))
	}
}

// SearchPeople finds accounts by username or display name prefix. Private
// accounts only turn up for their exact username (or when you already
// follow each other in some direction); blocks hide people both ways.
func (s *Service) SearchPeople(ctx context.Context, viewer, q string, limit int) ([]Person, error) {
	q = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "@")))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	like := escapeLike(q) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT `+personCols+` FROM users u
		WHERE u.disabled = 0 AND u.id != ?
		  AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = u.id AND b.blocked_id = ?) OR (b.blocker_id = ? AND b.blocked_id = u.id))
		  AND (? = '' OR LOWER(u.username) LIKE ? ESCAPE '\' OR LOWER(u.display_name) LIKE ? ESCAPE '\' OR LOWER(u.display_name) LIKE ? ESCAPE '\')
		  AND (u.visibility != 'private' OR LOWER(u.username) = ?
		       OR EXISTS (SELECT 1 FROM follows f WHERE (f.follower_id = ? AND f.followee_id = u.id) OR (f.follower_id = u.id AND f.followee_id = ?)))
		ORDER BY LOWER(u.username) = ? DESC, LOWER(u.username) LIMIT ?`,
		viewer, viewer, viewer, q, like, like, "% "+like, q, viewer, viewer, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
