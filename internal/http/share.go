package http

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"cato/internal/db"
)

// handleShare is the manifest's share_target (plan §8.3): Android's share
// sheet sends a page's title, text and URL here. A Steam or IGDB link to a
// game Cato knows opens its page; anything else opens the ⊕ sheet searched
// for the title worked out from the link or text.
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	http.Redirect(w, r, "/"+shareTarget(r.Context(), s.db, q.Get("title"), q.Get("text"), q.Get("url")), http.StatusSeeOther)
}

var (
	steamApp   = regexp.MustCompile(`^/app/(\d+)(?:/([^/?#]+))?`)
	eshopSlug  = regexp.MustCompile(`/(?:store/)?products?/([a-z0-9-]+?)(?:-(?:switch|switch-2|wii-u|3ds))?/?$`)
	igdbSlug   = regexp.MustCompile(`^/games/([a-z0-9-]+)`)
	firstURL   = regexp.MustCompile(`https?://\S+`)
	titleNoise = regexp.MustCompile(`(?i)\s*(\||–|-|—|:)?\s*(on steam|steam|playstation( store)?|ps[45] games?|nintendo( switch)?( - official site)?|xbox|official (launch |announcement |gameplay |reveal )?trailer|launch trailer|trailer|gameplay|youtube|igdb\.com|buy now|pre-?order now)\s*$`)
)

func shareTarget(ctx context.Context, database *db.DB, title, text, link string) string {
	if link == "" {
		// Some apps put the link in the text.
		if m := firstURL.FindString(text); m != "" {
			link = m
			text = strings.TrimSpace(strings.Replace(text, m, "", 1))
		}
	}
	guess := ""
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		switch {
		case host == "store.steampowered.com" || host == "steamcommunity.com":
			if m := steamApp.FindStringSubmatch(u.Path); m != nil {
				var id int64
				database.QueryRowContext(ctx, `SELECT MIN(game_id) FROM game_external_ids WHERE source = 1 AND uid = ?`, m[1]).Scan(&id)
				if id != 0 {
					return "#game/" + strconv.FormatInt(id, 10)
				}
				guess = strings.ReplaceAll(m[2], "_", " ")
			}
		case host == "igdb.com":
			if m := igdbSlug.FindStringSubmatch(u.Path); m != nil {
				var id int64
				database.QueryRowContext(ctx, `SELECT id FROM games WHERE slug = ?`, m[1]).Scan(&id)
				if id != 0 {
					return "#game/" + strconv.FormatInt(id, 10)
				}
				guess = strings.ReplaceAll(m[1], "-", " ")
			}
		case strings.HasSuffix(host, "nintendo.com"):
			if m := eshopSlug.FindStringSubmatch(u.Path); m != nil {
				guess = strings.ReplaceAll(m[1], "-", " ")
			}
		}
	}
	if guess == "" {
		guess = cleanSharedTitle(title)
	}
	if guess == "" {
		guess = cleanSharedTitle(text)
	}
	if len([]rune(guess)) > 100 {
		guess = string([]rune(guess)[:100])
	}
	if guess == "" {
		return "#log"
	}
	return "#log?q=" + url.QueryEscape(guess)
}

// cleanSharedTitle drops store and video suffixes ("… on Steam",
// "| Official Trailer") from a shared page title.
func cleanSharedTitle(t string) string {
	t = strings.TrimSpace(t)
	for i := 0; i < 4; i++ {
		next := strings.TrimSpace(titleNoise.ReplaceAllString(t, ""))
		if next == t {
			break
		}
		t = next
	}
	return strings.Trim(t, " -–|:")
}
