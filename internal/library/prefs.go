package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// Prefs are per-user settings kept in user_state.prefs_json. Unknown keys
// are dropped on write, so the stored document only ever holds these.
type Prefs struct {
	// Theme is system (default), light or dark.
	Theme string `json:"theme"`
	// RatingDisplay is stars (5 stars with halves, default), ten or hundred.
	RatingDisplay string `json:"rating_display"`
	// DefaultPlatforms pre-fill ownership when adding a game.
	DefaultPlatforms []string `json:"default_platforms"`
	// LibraryLayout is grid (default) or list; Density comfortable or compact.
	LibraryLayout string `json:"library_layout"`
	Density       string `json:"density"`
	// LibrarySort is the default library order.
	LibrarySort string `json:"library_sort"`
	// UpNext is the user's pinned queue of backlog games (at most 5).
	UpNext []int64 `json:"up_next"`
	// Streaks shows the gentle "played N days" line on Home.
	Streaks bool `json:"streaks"`
	// WeekStart is mon (default) or sun, for the diary calendar.
	WeekStart string `json:"week_start"`
	// OnboardingDone hides the first-run card.
	OnboardingDone bool `json:"onboarding_done"`
	// Haptics enables vibration feedback when logging (Android).
	Haptics bool `json:"haptics"`
	// Confetti celebrates finishing a game (reduced motion still wins).
	Confetti bool `json:"confetti"`
	// SavedViews are named Library filters (shelf, search, sort, layout).
	SavedViews []SavedView `json:"saved_views"`

	// Notify says which notification kinds also go out as a push or in
	// the email digest (the in-app inbox always has them). Social kinds are
	// off by default (plan §8.2).
	Notify map[string]Channels `json:"notify"`
	// EmailDigest sends a weekly email of unread notifications (when the
	// server has SMTP).
	EmailDigest bool `json:"email_digest"`
	// ReleaseAlerts notifies on the day a wishlist game comes out on your
	// platforms; ReleaseWeek also sends a heads-up a week before.
	ReleaseAlerts bool `json:"release_alerts"`
	ReleaseWeek   bool `json:"release_week"`
	// PriceAlerts watches wishlist games on IsThereAnyDeal.
	PriceAlerts PriceAlerts `json:"price_alerts"`
}

// Channels are where a notification kind is delivered besides the inbox.
type Channels struct {
	Push  bool `json:"push"`
	Email bool `json:"email"`
}

// PriceAlerts: a deal of at least MinCut percent, or (LowOnly) only a
// historical low, in Country's store.
type PriceAlerts struct {
	Enabled bool   `json:"enabled"`
	MinCut  int    `json:"min_cut"`
	LowOnly bool   `json:"low_only"`
	Country string `json:"country"`
}

// NotifyKinds are the notification kinds with delivery settings.
var NotifyKinds = []string{"release", "release_soon", "price", "follow", "follow_request", "follow_accepted", "like", "invite_used"}

func defaultNotify() map[string]Channels {
	return map[string]Channels{
		"release": {Push: true}, "release_soon": {Push: true}, "price": {Push: true},
		"follow": {}, "follow_request": {}, "follow_accepted": {}, "like": {}, "invite_used": {},
	}
}

// SavedView is a named Library route query ("shelf=backlog&q=$co-op").
type SavedView struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

const (
	maxSavedViews     = 20
	maxSavedViewName  = 40
	maxSavedViewQuery = 300
)

// DefaultPrefs is what a new account starts with.
func DefaultPrefs() Prefs {
	return Prefs{Theme: "system", RatingDisplay: "stars", DefaultPlatforms: []string{}, LibraryLayout: "grid",
		Density: "comfortable", LibrarySort: "updated", UpNext: []int64{}, Streaks: true, WeekStart: "mon",
		Haptics: true, Confetti: true, SavedViews: []SavedView{},
		Notify: defaultNotify(), ReleaseAlerts: true, PriceAlerts: PriceAlerts{MinCut: 50, Country: "US"}}
}

const maxUpNext = 5

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func (p *Prefs) validate() error {
	if !oneOf(p.Theme, "system", "light", "dark") {
		return invalid("invalid_pref", "theme", "Theme must be system, light or dark")
	}
	if !oneOf(p.RatingDisplay, "stars", "ten", "hundred") {
		return invalid("invalid_pref", "rating_display", "Rating display must be stars, ten or hundred")
	}
	if !oneOf(p.LibraryLayout, "grid", "list") {
		return invalid("invalid_pref", "library_layout", "Layout must be grid or list")
	}
	if !oneOf(p.Density, "comfortable", "compact") {
		return invalid("invalid_pref", "density", "Density must be comfortable or compact")
	}
	if NormalizeSort(p.LibrarySort) != p.LibrarySort {
		return invalid("invalid_pref", "library_sort", "Unknown sort")
	}
	if !oneOf(p.WeekStart, "mon", "sun") {
		return invalid("invalid_pref", "week_start", "Week start must be mon or sun")
	}
	st := itemState{status: StatusBacklog, platforms: p.DefaultPlatforms}
	if err := validateState(&st); err != nil {
		return invalid("invalid_pref", "default_platforms", err.Error())
	}
	p.DefaultPlatforms = st.platforms
	p.UpNext = dedupeIDs(p.UpNext)
	if p.UpNext == nil {
		p.UpNext = []int64{}
	}
	if len(p.UpNext) > maxUpNext {
		return invalid("invalid_pref", "up_next", "Up Next holds at most 5 games")
	}
	if p.SavedViews == nil {
		p.SavedViews = []SavedView{}
	}
	if len(p.SavedViews) > maxSavedViews {
		return invalid("invalid_pref", "saved_views", "At most 20 saved views")
	}
	for i, v := range p.SavedViews {
		v.Name = strings.TrimSpace(v.Name)
		if v.Name == "" || utf8.RuneCountInString(v.Name) > maxSavedViewName || len(v.Query) > maxSavedViewQuery {
			return invalid("invalid_pref", "saved_views", "A saved view needs a name of at most 40 characters")
		}
		p.SavedViews[i] = v
	}
	if p.Notify == nil {
		p.Notify = defaultNotify()
	}
	for k := range p.Notify {
		if !oneOf(k, NotifyKinds...) {
			return invalid("invalid_pref", "notify", "Unknown notification kind "+k)
		}
	}
	if p.PriceAlerts.MinCut < 0 || p.PriceAlerts.MinCut > 100 {
		return invalid("invalid_pref", "price_alerts", "The discount is 0 to 100%")
	}
	p.PriceAlerts.Country = strings.ToUpper(strings.TrimSpace(p.PriceAlerts.Country))
	if p.PriceAlerts.Country == "" {
		p.PriceAlerts.Country = "US"
	}
	if len(p.PriceAlerts.Country) != 2 {
		return invalid("invalid_pref", "price_alerts", "Use a two-letter country code")
	}
	return nil
}

// GetPrefs returns the user's settings over the defaults.
func (s *Store) GetPrefs(ctx context.Context, userID string) (Prefs, error) {
	return getPrefs(ctx, s.db, userID)
}

func getPrefs(ctx context.Context, q querier, userID string) (Prefs, error) {
	p := DefaultPrefs()
	var raw string
	err := q.QueryRowContext(ctx, `SELECT prefs_json FROM user_state WHERE user_id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal([]byte(raw), &p)
	if p.validate() != nil {
		// A stored value from an older release: fall back field by field.
		d := DefaultPrefs()
		if !oneOf(p.Theme, "system", "light", "dark") {
			p.Theme = d.Theme
		}
		if !oneOf(p.RatingDisplay, "stars", "ten", "hundred") {
			p.RatingDisplay = d.RatingDisplay
		}
		if !oneOf(p.LibraryLayout, "grid", "list") {
			p.LibraryLayout = d.LibraryLayout
		}
		if !oneOf(p.Density, "comfortable", "compact") {
			p.Density = d.Density
		}
		p.LibrarySort = NormalizeSort(p.LibrarySort)
		if !oneOf(p.WeekStart, "mon", "sun") {
			p.WeekStart = d.WeekStart
		}
		if len(p.UpNext) > maxUpNext {
			p.UpNext = p.UpNext[:maxUpNext]
		}
	}
	return p, nil
}

// UpdatePrefs merges patch (a JSON object of Prefs fields) into the stored
// settings and returns the result.
func (s *Store) UpdatePrefs(ctx context.Context, userID string, patch json.RawMessage) (Prefs, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Prefs{}, err
	}
	defer tx.Rollback()
	p, err := getPrefs(ctx, tx, userID)
	if err != nil {
		return p, err
	}
	dec := json.NewDecoder(strings.NewReader(string(patch)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, invalid("invalid_pref", "", "Unknown or malformed setting")
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	b, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_state (user_id, prefs_json) VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET prefs_json = excluded.prefs_json`, userID, string(b)); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
