package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// RelatedGame is a game shown on another game's page.
type RelatedGame struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Cover       string `json:"cover"`
	ReleaseDate int64  `json:"release_date"`
	Kind        string `json:"kind,omitempty"`
}

type SeriesInfo struct {
	ID    int64         `json:"id"`
	Kind  string        `json:"kind"`
	Name  string        `json:"name"`
	Games []RelatedGame `json:"games"`
}

type ReleaseInfo struct {
	Platform string `json:"platform"`
	Date     int64  `json:"date"`
	Human    string `json:"human"`
	Region   string `json:"region"`
}

type TTB struct {
	Hastily    int64 `json:"hastily"`
	Normally   int64 `json:"normally"`
	Completely int64 `json:"completely"`
	Count      int64 `json:"count"`
}

type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Kind  string `json:"kind"`
}

// Extras is everything the game page shows beyond the core row.
type Extras struct {
	TTB              *TTB                     `json:"ttb"`
	Genres           []string                 `json:"genres"`
	Themes           []string                 `json:"themes"`
	Modes            []string                 `json:"modes"`
	Perspectives     []string                 `json:"perspectives"`
	GameType         string                   `json:"game_type"`
	Developers       []string                 `json:"developers"`
	Publishers       []string                 `json:"publishers"`
	Series           []SeriesInfo             `json:"series"`
	Related          map[string][]RelatedGame `json:"related"`
	ReleaseDates     []ReleaseInfo            `json:"release_dates"`
	Screenshots      []string                 `json:"screenshots"`
	Artworks         []string                 `json:"artworks"`
	Videos           []Video                  `json:"videos"`
	Links            []Link                   `json:"links"`
	AgeRatings       []string                 `json:"age_ratings"`
	Languages        []string                 `json:"languages"`
	Multiplayer      *Multiplayer             `json:"multiplayer"`
	DetailsFetchedAt int64                    `json:"details_fetched_at"`
	Source           string                   `json:"source"`
	Editions         []RelatedGame            `json:"editions"`
	BaseGame         *RelatedGame             `json:"base_game"`
}

var regionNames = map[int64]string{1: "Europe", 2: "North America", 3: "Australia", 4: "New Zealand", 5: "Japan",
	6: "China", 7: "Asia", 8: "Worldwide", 9: "Korea", 10: "Brazil"}

// websiteKinds maps IGDB website types to a label and a stable kind.
var websiteKinds = map[int64][2]string{
	1: {"Official site", "official"}, 2: {"Fandom wiki", "wiki"}, 3: {"Wikipedia", "wikipedia"}, 4: {"Facebook", "facebook"},
	5: {"X (Twitter)", "twitter"}, 6: {"Twitch", "twitch"}, 8: {"Instagram", "instagram"}, 9: {"YouTube", "youtube"},
	10: {"App Store (iPhone)", "app_store"}, 11: {"App Store (iPad)", "app_store"}, 12: {"Google Play", "google_play"},
	13: {"Steam", "steam"}, 14: {"Reddit", "reddit"}, 15: {"itch.io", "itch"}, 16: {"Epic Games Store", "epic"},
	17: {"GOG", "gog"}, 18: {"Discord", "discord"}, 19: {"Bluesky", "bluesky"}, 22: {"Xbox", "xbox"},
	23: {"PlayStation Store", "playstation"}, 24: {"Nintendo eShop", "nintendo"}, 25: {"Meta Quest", "meta"},
}

// storeKinds are "where to get it" links, in display order.
var storeKinds = []string{"steam", "gog", "epic", "playstation", "xbox", "nintendo", "itch", "app_store", "google_play", "meta", "official"}

// Wikidata-derived outbound links (PCGamingWiki, HowLongToBeat) are added
// by the crosswalk job as website types >= 1000.
const (
	WebsiteHLTB         = 1001
	WebsitePCGamingWiki = 1002
)

func init() {
	websiteKinds[WebsiteHLTB] = [2]string{"HowLongToBeat", "hltb"}
	websiteKinds[WebsitePCGamingWiki] = [2]string{"PCGamingWiki", "pcgamingwiki"}
}

func namesOf(raw string, names map[int64]string) []string {
	var ids []int64
	_ = json.Unmarshal([]byte(raw), &ids)
	out := []string{}
	for _, id := range ids {
		if n, ok := names[id]; ok {
			out = append(out, n)
		}
	}
	return out
}

func coverOf(local, remote string) string {
	if local != "" {
		return local
	}
	return remote
}

// seriesSQL lists every local game in the same collections as a game, by
// release date.
const seriesSQL = `SELECT gs.series_id, gs.kind, gs.name, g.id, g.name, g.local_cover_path, g.cover_url, g.first_release_date
		FROM game_series mine
		JOIN game_series gs ON gs.kind = mine.kind AND gs.series_id = mine.series_id
		JOIN games g ON g.id = gs.game_id
		WHERE mine.game_id = ? AND g.version_parent = 0
		ORDER BY gs.kind, gs.series_id, CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END, g.first_release_date`

// editionsSQL lists a game's editions (IGDB version_parent). The
// "!= 0" lets it use idx_games_version_parent, which leaves out base games.
const editionsSQL = `SELECT id, name, local_cover_path, cover_url, first_release_date FROM games WHERE version_parent = ? AND version_parent != 0 ORDER BY first_release_date LIMIT 20`

// GameExtras loads the rich data for a game page.
func (s *Store) GameExtras(ctx context.Context, id int64) (*Extras, error) {
	x := &Extras{Genres: []string{}, Themes: []string{}, Modes: []string{}, Perspectives: []string{}, Developers: []string{},
		Publishers: []string{}, Series: []SeriesInfo{}, Related: map[string][]RelatedGame{}, ReleaseDates: []ReleaseInfo{},
		Screenshots: []string{}, Artworks: []string{}, Videos: []Video{}, Links: []Link{}, AgeRatings: []string{}, Languages: []string{},
		Editions: []RelatedGame{}}
	var genres, themes, modes, persp string
	var ttb TTB
	var category, versionParent int64
	if err := s.db.QueryRowContext(ctx, `SELECT genres_json, themes_json, modes_json, perspectives_json, ttb_hastily, ttb_normally,
			ttb_completely, ttb_count, details_fetched_at, source, category, version_parent FROM games WHERE id = ?`, id).
		Scan(&genres, &themes, &modes, &persp, &ttb.Hastily, &ttb.Normally, &ttb.Completely, &ttb.Count, &x.DetailsFetchedAt, &x.Source,
			&category, &versionParent); err != nil {
		return nil, err
	}
	if ttb.Normally > 0 || ttb.Hastily > 0 || ttb.Completely > 0 {
		x.TTB = &ttb
	}
	lookup := func(kind, raw string) []string {
		names, err := s.LookupNames(ctx, kind)
		if err != nil {
			return []string{}
		}
		return namesOf(raw, names)
	}
	x.Genres = lookup("genres", genres)
	x.Themes = lookup("themes", themes)
	x.Modes = lookup("game_modes", modes)
	x.Perspectives = lookup("player_perspectives", persp)
	// Only non-main games show their type ("DLC", "Remaster", …).
	if names, err := s.LookupNames(ctx, "game_types"); err == nil && category != 0 {
		x.GameType = names[category]
	}

	rows, err := s.db.QueryContext(ctx, `SELECT name, developer, publisher FROM game_companies WHERE game_id = ? ORDER BY developer DESC, name`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var dev, pub bool
		if err := rows.Scan(&name, &dev, &pub); err != nil {
			rows.Close()
			return nil, err
		}
		if dev {
			x.Developers = append(x.Developers, name)
		}
		if pub {
			x.Publishers = append(x.Publishers, name)
		}
	}
	rows.Close()

	// Series: every local game in the same collection, by release date.
	rows, err = s.db.QueryContext(ctx, seriesSQL, id)
	if err != nil {
		return nil, err
	}
	bySeries := map[string]*SeriesInfo{}
	var order []string
	for rows.Next() {
		var sid int64
		var kind, sname, local, remote string
		var rg RelatedGame
		if err := rows.Scan(&sid, &kind, &sname, &rg.ID, &rg.Name, &local, &remote, &rg.ReleaseDate); err != nil {
			rows.Close()
			return nil, err
		}
		rg.Cover = coverOf(local, remote)
		key := kind + ":" + itoa(sid)
		si, ok := bySeries[key]
		if !ok {
			si = &SeriesInfo{ID: sid, Kind: kind, Name: sname}
			bySeries[key] = si
			order = append(order, key)
		}
		si.Games = append(si.Games, rg)
	}
	rows.Close()
	for _, k := range order {
		// A franchise that only repeats a collection adds nothing.
		if si := bySeries[k]; len(si.Games) > 1 {
			x.Series = append(x.Series, *si)
		}
	}

	rows, err = s.db.QueryContext(ctx, `SELECT r.kind, g.id, g.name, g.local_cover_path, g.cover_url, g.first_release_date
		FROM game_relations r JOIN games g ON g.id = r.related_id
		WHERE r.game_id = ? ORDER BY r.kind, g.popularity_score DESC, g.name LIMIT 120`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rg RelatedGame
		var local, remote string
		if err := rows.Scan(&rg.Kind, &rg.ID, &rg.Name, &local, &remote, &rg.ReleaseDate); err != nil {
			rows.Close()
			return nil, err
		}
		rg.Cover = coverOf(local, remote)
		x.Related[rg.Kind] = append(x.Related[rg.Kind], rg)
	}
	rows.Close()

	platformNames, _ := s.PlatformNames(ctx)
	rows, err = s.db.QueryContext(ctx, `SELECT platform_id, date, region, human FROM game_release_dates WHERE game_id = ? ORDER BY date`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r ReleaseDate
		if err := rows.Scan(&r.PlatformID, &r.Date, &r.Region, &r.Human); err != nil {
			rows.Close()
			return nil, err
		}
		x.ReleaseDates = append(x.ReleaseDates, ReleaseInfo{Platform: platformNames[r.PlatformID], Date: r.Date, Human: r.Human, Region: regionNames[r.Region]})
	}
	rows.Close()

	// Editions: the base game, or this game's editions.
	if versionParent != 0 {
		var b RelatedGame
		var local, remote string
		if err := s.db.QueryRowContext(ctx, `SELECT id, name, local_cover_path, cover_url, first_release_date FROM games WHERE id = ?`, versionParent).
			Scan(&b.ID, &b.Name, &local, &remote, &b.ReleaseDate); err == nil {
			b.Cover = coverOf(local, remote)
			x.BaseGame = &b
		}
	}
	rows, err = s.db.QueryContext(ctx, editionsSQL, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e RelatedGame
		var local, remote string
		if err := rows.Scan(&e.ID, &e.Name, &local, &remote, &e.ReleaseDate); err != nil {
			rows.Close()
			return nil, err
		}
		e.Cover = coverOf(local, remote)
		x.Editions = append(x.Editions, e)
	}
	rows.Close()

	var doc string
	err = s.db.QueryRowContext(ctx, `SELECT json FROM game_details WHERE game_id = ?`, id).Scan(&doc)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if doc != "" {
		var d Details
		if json.Unmarshal([]byte(doc), &d) == nil {
			x.Screenshots, x.Artworks, x.Videos = nonNil(d.Screenshots), nonNil(d.Artworks), d.Videos
			if x.Videos == nil {
				x.Videos = []Video{}
			}
			x.AgeRatings, x.Languages, x.Multiplayer = nonNil(d.AgeRatings), nonNil(d.Languages), d.Multiplayer
			x.Links = linksOf(d.Websites)
		}
	}
	// Outbound links found through the Wikidata crosswalk.
	rows, err = s.db.QueryContext(ctx, `SELECT source, uid FROM game_external_ids WHERE game_id = ? AND source IN (?, ?) ORDER BY source`,
		id, WebsiteHLTB, WebsitePCGamingWiki)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var src int64
		var uid string
		if rows.Scan(&src, &uid) != nil || uid == "" {
			continue
		}
		switch src {
		case WebsiteHLTB:
			x.Links = append(x.Links, Link{Label: "HowLongToBeat", URL: "https://howlongtobeat.com/game/" + url.PathEscape(uid), Kind: "hltb"})
		case WebsitePCGamingWiki:
			x.Links = append(x.Links, Link{Label: "PCGamingWiki", URL: "https://www.pcgamingwiki.com/wiki/" + url.PathEscape(strings.ReplaceAll(uid, " ", "_")), Kind: "pcgamingwiki"})
		}
	}
	return x, rows.Err()
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// linksOf orders store links first, then the rest, one per kind.
func linksOf(sites []Website) []Link {
	byKind := map[string]Link{}
	var other []Link
	for _, w := range sites {
		if !strings.HasPrefix(w.URL, "https://") && !strings.HasPrefix(w.URL, "http://") {
			continue
		}
		k, ok := websiteKinds[w.Type]
		if !ok {
			continue
		}
		l := Link{Label: k[0], URL: w.URL, Kind: k[1]}
		if _, dup := byKind[k[1]]; !dup {
			byKind[k[1]] = l
			isStore := false
			for _, sk := range storeKinds {
				if sk == k[1] {
					isStore = true
				}
			}
			if !isStore {
				other = append(other, l)
			}
		}
	}
	out := []Link{}
	for _, k := range storeKinds {
		if l, ok := byKind[k]; ok {
			out = append(out, l)
		}
	}
	sort.SliceStable(other, func(i, j int) bool { return other[i].Label < other[j].Label })
	return append(out, other...)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
