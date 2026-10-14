package catalog

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// richFake is an IGDB client with the rich-data endpoints.
type richFake struct {
	fakeIGDB
	mu          sync.Mutex
	details     map[int64]Details
	ttb         map[int64]TimeToBeat
	updated     []Game
	popular     []Popularity
	full        map[int64]Game
	detailCalls int
}

func (f *richFake) GetDetails(ctx context.Context, ids []int64) ([]Details, error) {
	f.mu.Lock()
	f.detailCalls++
	f.mu.Unlock()
	var out []Details
	for _, id := range ids {
		if d, ok := f.details[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *richFake) GetTimesToBeat(ctx context.Context, ids []int64) ([]TimeToBeat, error) {
	var out []TimeToBeat
	for _, id := range ids {
		if t, ok := f.ttb[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *richFake) GetLookup(ctx context.Context, kind string) ([]NamedRef, error) {
	switch kind {
	case "genres":
		return []NamedRef{{ID: 12, Name: "Role-playing (RPG)"}, {ID: 31, Name: "Adventure"}}, nil
	case "themes":
		return []NamedRef{{ID: 17, Name: "Fantasy"}}, nil
	case "game_types":
		return []NamedRef{{ID: 0, Name: "Main Game"}}, nil
	}
	return nil, nil
}

func (f *richFake) GetUpdatedSince(ctx context.Context, since int64, offset, limit int) ([]Game, error) {
	var out []Game
	for _, g := range f.updated {
		if g.SourceUpdatedAt > since {
			out = append(out, g)
		}
	}
	if offset >= len(out) {
		return nil, nil
	}
	return out[offset:], nil
}

func (f *richFake) GetPopular(ctx context.Context, t int64, limit int) ([]Popularity, error) {
	if t != PopularityVisits {
		return nil, nil
	}
	return f.popular, nil
}

func (f *richFake) GetGamesFull(ctx context.Context, ids []int64) ([]Game, error) {
	var out []Game
	for _, id := range ids {
		if g, ok := f.full[id]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

func insertGames(t *testing.T, s *Store, games ...Game) {
	t.Helper()
	for _, g := range games {
		if g.Slug == "" {
			g.Slug = strings.ToLower(strings.ReplaceAll(g.Name, " ", "-"))
		}
		g.NormalizedName = NormalizeName(g.Name)
		if g.PlatformsJSON == "" {
			g.PlatformsJSON = "[]"
		}
		if g.GenresJSON == "" {
			g.GenresJSON = "[]"
		}
		if err := s.UpsertIGDBGame(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetailsAreStoredAndShownOnTheGamePage(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	insertGames(t, store, Game{ID: 1, Name: "Breath of the Wild", FirstReleaseDate: 1488499200},
		Game{ID: 2, Name: "Tears of the Kingdom", FirstReleaseDate: 1683849600},
		Game{ID: 3, Name: "Okami"}, Game{ID: 4, Name: "BotW Expansion Pass"},
		Game{ID: 5, Name: "Breath of the Wild Deluxe", VersionParent: 1})
	fake := &richFake{
		details: map[int64]Details{
			1: {GameID: 1, Genres: []int64{12, 31}, Themes: []int64{17},
				Companies:    []Company{{ID: 70, Name: "Nintendo EPD", Developer: true}, {ID: 71, Name: "Nintendo", Publisher: true}},
				Collections:  []NamedRef{{ID: 106, Name: "The Legend of Zelda"}},
				Related:      map[string][]int64{"similar": {3, 999}, "expansion": {4}},
				ReleaseDates: []ReleaseDate{{PlatformID: 130, Date: 1488499200, Region: 8, Human: "Mar 03, 2017"}},
				External:     []ExternalID{{Source: 1, UID: "12345"}},
				Screenshots:  []string{"sc1"}, Artworks: []string{"ar1"}, Videos: []Video{{VideoID: "yt1", Name: "Trailer"}},
				Websites:   []Website{{Type: 1, URL: "https://zelda.com"}, {Type: 13, URL: "https://store.steampowered.com/app/1"}, {Type: 17, URL: "javascript:alert(1)"}},
				AgeRatings: []string{"PEGI 12"}, Languages: []string{"English"}, Multiplayer: &Multiplayer{OnlineCoop: true}},
			2: {GameID: 2, Collections: []NamedRef{{ID: 106, Name: "The Legend of Zelda"}}},
		},
		ttb: map[int64]TimeToBeat{1: {GameID: 1, Hastily: 36000, Normally: 180000, Completely: 540000, Count: 9}},
	}
	svc := NewService(store, fake, database)
	defer svc.Shutdown(ctx)
	if err := svc.SyncLookups(ctx); err != nil {
		t.Fatal(err)
	}
	// The first view of a game fetches its details before answering.
	svc.EnsureDetails(ctx, 1, time.Second)
	svc.RefreshDetails(ctx, []int64{2})
	x, err := store.GameExtras(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if x.TTB == nil || x.TTB.Normally != 3000 || x.TTB.Hastily != 600 {
		t.Errorf("ttb minutes %+v", x.TTB)
	}
	if strings.Join(x.Genres, ",") != "Role-playing (RPG),Adventure" || strings.Join(x.Themes, ",") != "Fantasy" || x.GameType != "" {
		t.Errorf("names %v %v %q", x.Genres, x.Themes, x.GameType)
	}
	if strings.Join(x.Developers, ",") != "Nintendo EPD" || strings.Join(x.Publishers, ",") != "Nintendo" {
		t.Errorf("companies %v %v", x.Developers, x.Publishers)
	}
	if len(x.Series) != 1 || len(x.Series[0].Games) != 2 || x.Series[0].Games[0].ID != 1 || x.Series[0].Games[1].ID != 2 {
		t.Errorf("series in release order %+v", x.Series)
	}
	if len(x.Related["similar"]) != 1 || x.Related["similar"][0].Name != "Okami" || len(x.Related["expansion"]) != 1 {
		t.Errorf("related shows only known games %+v", x.Related)
	}
	if len(x.ReleaseDates) != 1 || x.ReleaseDates[0].Region != "Worldwide" {
		t.Errorf("release dates %+v", x.ReleaseDates)
	}
	if len(x.Links) != 2 || x.Links[0].Kind != "steam" || x.Links[1].Kind != "official" {
		t.Errorf("links: stores first, unsafe URLs dropped: %+v", x.Links)
	}
	if len(x.Editions) != 1 || x.Editions[0].ID != 5 {
		t.Errorf("editions %+v", x.Editions)
	}
	if x5, _ := store.GameExtras(ctx, 5); x5.BaseGame == nil || x5.BaseGame.ID != 1 {
		t.Errorf("edition's base game %+v", x5.BaseGame)
	}
	if x.Multiplayer == nil || !x.Multiplayer.OnlineCoop || x.Videos[0].VideoID != "yt1" {
		t.Errorf("display data %+v", x)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_external_ids WHERE source = 1 AND uid = '12345' AND game_id = 1`).Scan(&n)
	if n != 1 {
		t.Error("external IDs (Steam app IDs) not stored")
	}
	// A fresh game page view doesn't refetch.
	calls := fake.detailCalls
	svc.EnsureDetails(ctx, 1, time.Second)
	if fake.detailCalls != calls {
		t.Error("fresh details were refetched")
	}
}

func TestDeltaSyncAndPopularity(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	now := time.Now().Unix()
	fake := &richFake{
		updated: []Game{
			{ID: 10, Name: "Old Change", Slug: "a", NormalizedName: "old change", PlatformsJSON: "[]", GenresJSON: "[]", SourceUpdatedAt: now - 7200},
			{ID: 11, Name: "New Game", Slug: "b", NormalizedName: "new game", PlatformsJSON: "[]", GenresJSON: "[]", SourceUpdatedAt: now - 60},
		},
		popular: []Popularity{{GameID: 11, Type: 1, Value: 0.5}, {GameID: 12, Type: 1, Value: 0.4}},
		full:    map[int64]Game{12: {ID: 12, Name: "Trending Unknown", Slug: "c", NormalizedName: "trending unknown", PlatformsJSON: "[]", GenresJSON: "[]"}},
	}
	svc := NewService(store, fake, database)
	defer svc.Shutdown(ctx)
	if err := svc.DeltaSync(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE id IN (10, 11)`).Scan(&count)
	if count != 2 {
		t.Fatalf("delta upserted %d games", count)
	}
	since, _ := store.syncState(ctx, "delta_since")
	if since != itoa(now-60) {
		t.Errorf("delta_since %q, want the newest updated_at", since)
	}
	if err := svc.SyncPopularity(ctx); err != nil {
		t.Fatal(err)
	}
	var name string
	var pop float64
	database.QueryRow(`SELECT name, igdb_popularity FROM games WHERE id = 12`).Scan(&name, &pop)
	if name != "Trending Unknown" || pop != 0.4 {
		t.Errorf("trending game fetched? %q %v", name, pop)
	}

	// The jobs: rich ones replace the refresh-by-age loop.
	var names []string
	for _, j := range svc.Jobs(true) {
		names = append(names, j.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "catalog.delta-sync") || strings.Contains(joined, "catalog.stale-refresh") || !strings.Contains(joined, "catalog.library-refresh") {
		t.Errorf("jobs %v", names)
	}
}

func TestLibraryRefreshCoversStaleLibraryGames(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	insertGames(t, store, Game{ID: 1, Name: "In Library"}, Game{ID: 2, Name: "Not In Library"})
	putInLibrary(t, database, 1)
	fake := &richFake{details: map[int64]Details{1: {GameID: 1, Genres: []int64{12}}, 2: {GameID: 2}},
		full: map[int64]Game{1: {ID: 1, Name: "In Library (Renamed)", Slug: "x", NormalizedName: "in library renamed", PlatformsJSON: "[]", GenresJSON: "[]"}}}
	svc := NewService(store, fake, database)
	defer svc.Shutdown(ctx)
	if err := svc.RefreshLibraryDetails(ctx); err != nil {
		t.Fatal(err)
	}
	var at1, at2 int64
	var name string
	database.QueryRow(`SELECT details_fetched_at, name FROM games WHERE id = 1`).Scan(&at1, &name)
	database.QueryRow(`SELECT details_fetched_at FROM games WHERE id = 2`).Scan(&at2)
	if at1 == 0 || at2 != 0 || name != "In Library (Renamed)" {
		t.Errorf("library game %d %q, other %d", at1, name, at2)
	}
}
