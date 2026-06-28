package games

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"cato/internal/db"

	_ "modernc.org/sqlite"
)

type fakeIGDB struct {
	searchCalls int
	searchFunc  func(ctx context.Context, query string, limit int) ([]Game, error)
}

func (f *fakeIGDB) SearchGames(ctx context.Context, query string, limit int) ([]Game, error) {
	f.searchCalls++
	if f.searchFunc != nil {
		return f.searchFunc(ctx, query, limit)
	}
	return nil, nil
}

func (f *fakeIGDB) GetGame(ctx context.Context, id int64) (*Game, error) {
	return nil, nil
}

func setupGameDB(t *testing.T) (*db.DB, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database, NewStore(database)
}

func TestSearchLocal(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, aggregated_rating_count, aggregated_rating) VALUES
		(1, 'The Legend of Zelda', 'the-legend-of-zelda', 'the legend of zelda', 536457600, 500, 95),
		(2, 'Zelda II: The Adventure of Link', 'zelda-ii', 'zelda ii the adventure of link', 567993600, 200, 75),
		(3, 'Super Mario Bros', 'super-mario-bros', 'super mario bros', 504921600, 1000, 90)`)

	t.Run("exact match first", func(t *testing.T) {
		results, err := store.SearchLocal(context.Background(), "the legend of zelda", 10)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results")
		}
		if results[0].Name != "The Legend of Zelda" {
			t.Errorf("expected Zelda first, got %q", results[0].Name)
		}
	})

	t.Run("partial match", func(t *testing.T) {
		results, err := store.SearchLocal(context.Background(), "zelda", 10)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) < 2 {
			t.Fatalf("expected at least 2 results, got %d", len(results))
		}
	})

	t.Run("no match", func(t *testing.T) {
		results, err := store.SearchLocal(context.Background(), "nonexistent", 10)
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})
}

func TestSearchCallsIGDBForNewQueryEvenWithLocalResults(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, aggregated_rating_count, aggregated_rating) VALUES
		(1, 'Zelda 1', 'zelda-1', 'zelda 1', 1, 10, 80),
		(2, 'Zelda 2', 'zelda-2', 'zelda 2', 2, 20, 85),
		(3, 'Zelda 3', 'zelda-3', 'zelda 3', 3, 30, 90),
		(4, 'Zelda 4', 'zelda-4', 'zelda 4', 4, 40, 95),
		(5, 'Zelda 5', 'zelda-5', 'zelda 5', 5, 50, 100)`)

	igdb := &fakeIGDB{}
	svc := NewService(store, igdb, database)

	results, err := svc.Search(context.Background(), "zelda")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d", len(results))
	}
	if igdb.searchCalls != 1 {
		t.Errorf("expected 1 IGDB call for new query, got %d", igdb.searchCalls)
	}
}

func TestSearchCallsIGDBWhenLocalResultsAreWeak(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date) VALUES
		(1, 'Only Match', 'only-match', 'only match', 1)`)

	igdb := &fakeIGDB{
		searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
			return []Game{
				{ID: 100, Name: "New Game", Slug: "new-game", NormalizedName: "new game", SafeName: "New Game"},
			}, nil
		},
	}
	svc := NewService(store, igdb, database)

	results, err := svc.Search(context.Background(), "only match")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if igdb.searchCalls != 1 {
		t.Errorf("expected 1 IGDB call, got %d", igdb.searchCalls)
	}
	_ = results
}

func TestSearchDoesNotCallIGDBForShortQueries(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	igdb := &fakeIGDB{}
	svc := NewService(store, igdb, database)

	// Query < 3 chars, even with 0 local results, IGDB should not be called
	_, _ = svc.Search(context.Background(), "ab")
	if igdb.searchCalls != 0 {
		t.Errorf("expected no IGDB calls for query length < 3, got %d", igdb.searchCalls)
	}
}

func TestSearchIGDBFailureDoesNotBreakLocal(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date) VALUES
		(1, 'Only Match', 'only-match', 'only match', 1)`)

	igdb := &fakeIGDB{
		searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
			return nil, fmt.Errorf("igdb error")
		},
	}
	svc := NewService(store, igdb, database)

	results, err := svc.Search(context.Background(), "only match")
	if err != nil {
		t.Fatalf("search should not fail when IGDB fails: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 local result, got %d", len(results))
	}
}

func TestSearchCachesIGDBAndSkipsOnRepeat(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	igdb := &fakeIGDB{
		searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
			return []Game{
				{ID: 200, Name: "Cached Result", Slug: "cached", NormalizedName: "cached result", SafeName: "Cached Result"},
			}, nil
		},
	}
	svc := NewService(store, igdb, database)

	results, err := svc.Search(context.Background(), "cached result")
	if err != nil {
		t.Fatalf("first search failed: %v", err)
	}
	if igdb.searchCalls != 1 {
		t.Errorf("first search: expected 1 IGDB call, got %d", igdb.searchCalls)
	}
	_ = results

	results2, err2 := svc.Search(context.Background(), "cached result")
	if err2 != nil {
		t.Fatalf("second search failed: %v", err2)
	}
	if igdb.searchCalls != 1 {
		t.Errorf("second search: expected still 1 IGDB call (cached), got %d", igdb.searchCalls)
	}
	_ = results2
}

func TestGetGameByID(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, summary) VALUES
		(1, 'Test Game', 'test-game', 'test game', 'A test game')`)

	game, err := store.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("get by id failed: %v", err)
	}
	if game == nil {
		t.Fatal("expected game")
	}
	if game.Name != "Test Game" {
		t.Errorf("expected 'Test Game', got %q", game.Name)
	}
}

func TestGetGameByIDNotFound(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	game, err := store.GetByID(context.Background(), 99999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if game != nil {
		t.Error("expected nil for non-existent game")
	}
}

func TestUpsertIGDBGame(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	g := Game{
		ID: 1, Name: "Test", Slug: "test", SafeName: "Test",
		NormalizedName: "test", PlatformsJSON: "[1,2]", GenresJSON: "[5]",
	}
	if err := store.UpsertIGDBGame(context.Background(), g); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	g2 := Game{
		ID: 1, Name: "Test Updated", Slug: "test-updated", SafeName: "Test Updated",
		NormalizedName: "test updated", PlatformsJSON: "[3]", GenresJSON: "[6]",
	}
	if err := store.UpsertIGDBGame(context.Background(), g2); err != nil {
		t.Fatalf("upsert update failed: %v", err)
	}

	game, _ := store.GetByID(context.Background(), 1)
	if game.Name != "Test Updated" {
		t.Errorf("expected 'Test Updated', got %q", game.Name)
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"The Legend of Zelda", "the legend of zelda"},
		{"Zelda's Adventure", "zeldas adventure"},
		{"Spider-Man: Miles Morales", "spider man miles morales"},
	}
	for _, tt := range tests {
		got := NormalizeName(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSearchLocalRanksByPopularityScore(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// Two games whose normalized names both match "zelda". The obscure one
	// has higher aggregated_rating_count (the old sort key) but the popular
	// one has a much higher popularity_score (follows-weighted). Popularity
	// must win within the same name-match tier.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, aggregated_rating_count, popularity_score, category) VALUES
		(1, 'Zelda Popular', 'zelda-popular', 'zelda popular', 50, 5000, 0),
		(2, 'Zelda Obscure', 'zelda-obscure', 'zelda obscure', 9999, 5, 0)`)

	results, err := store.SearchLocal(context.Background(), "zelda", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != 1 {
		t.Errorf("expected popular game (id=1) first, got id=%d", results[0].ID)
	}
}

func TestSearchLocalDeprioritizesDLC(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// Same name-match tier, same popularity_score. The main game
	// (category=0) must rank above the DLC (category=1).
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score, category) VALUES
		(10, 'Zelda DLC', 'zelda-dlc', 'zelda dlc', 1000, 1),
		(11, 'Zelda Main', 'zelda-main', 'zelda main', 1000, 0)`)

	results, err := store.SearchLocal(context.Background(), "zelda", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != 11 {
		t.Errorf("expected main game (id=11) before DLC, got id=%d", results[0].ID)
	}
}

func TestSearchLocalSubstringFragmentMatching(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'The Legend of Zelda', 'the-legend-of-zelda', 'the legend of zelda')`)

	// The trigram tokenizer matches on shared 3-char substrings, so a query
	// fragment that is a contiguous substring of the indexed name matches —
	// including fragments that span word boundaries mid-word. This is what
	// replaces the old LIKE '%...%' scan but via the FTS index.
	for _, q := range []string{"zeld", "egend of zel", "the legend of zelda"} {
		results, err := store.SearchLocal(context.Background(), q, 10)
		if err != nil {
			t.Fatalf("search %q failed: %v", q, err)
		}
		if len(results) != 1 || results[0].ID != 1 {
			t.Errorf("expected match for substring %q, got %v", q, results)
		}
	}
}

func TestSearchLocalShortQueryFallsBackToLike(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'Go', 'go', 'go')`)

	// 2-char queries can't use the trigram tokenizer; must fall back to LIKE.
	results, err := store.SearchLocal(context.Background(), "go", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result via LIKE fallback for 2-char query, got %d", len(results))
	}
}

func TestComputePopularityScore(t *testing.T) {
	tests := []struct {
		name             string
		follows, hypes   int64
		totalRatingCount int64
		category, status int64
		want             int64
	}{
		{"released main game with follows", 100, 50, 200, 0, 0, 100*3 + 50*2 + 200 + 10},
		{"DLC gets no bonus", 100, 50, 200, 1, 0, 100*3 + 50*2 + 200},
		{"unreleased gets no bonus", 100, 50, 200, 0, 2, 100*3 + 50*2 + 200},
		{"zeros", 0, 0, 0, 0, 0, 10},
	}
	for _, tt := range tests {
		got := ComputePopularityScore(tt.follows, tt.hypes, tt.totalRatingCount, tt.category, tt.status)
		if got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestBuildFTSMatch(t *testing.T) {
	tests := []struct {
		query string
		match string
		ok    bool
	}{
		{"ab", "", false},          // < 3 chars
		{"zelda", `"zelda"`, true}, // single token
		{"the legend of zelda", `"the legend of zelda"`, true}, // short tokens kept
		{`zelda"`, `"zelda"`, true},                            // special char stripped
	}
	for _, tt := range tests {
		match, ok := BuildFTSMatch(tt.query)
		if match != tt.match || ok != tt.ok {
			t.Errorf("BuildFTSMatch(%q) = (%q, %v), want (%q, %v)", tt.query, match, ok, tt.match, tt.ok)
		}
	}
}

func TestSearchLocalPagedFloorOptIn(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// Insert a strong match (prefix match — starts with "zeld").
	// This should always be returned regardless of floor.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score, category) VALUES
		(1, 'Zelda Test Strong', 'zelda-test-strong', 'zelda test strong', 0, 0)`)

	// Insert an obscure tier-3 substring match (contains "zeld" but doesn't start with it
	// and doesn't have a space before it, so matches only via substring LIKE, not prefix/word-prefix).
	// This should only be returned if popularity_score > 0 when floor is applied,
	// or always returned when floor is not applied.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score, category) VALUES
		(2, 'Puzzzeldic Game', 'puzzzeldic-game', 'puzzzeldic game', 0, 0)`)

	// Query for "zeld":
	// - "zelda test strong" matches via prefix (tier 2)
	// - "puzzzeldic game" matches only via tier-3 substring (contains "zeld" but no space before it)
	query := "zeld"

	// Without floor: both should be returned
	results, err := store.SearchLocalPaged(context.Background(), query, 10, 0, false)
	if err != nil {
		t.Fatalf("search without floor failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("without floor: expected 2 results, got %d", len(results))
	}

	// With floor: only strong match (id=1) should be returned
	// because the second game has popularity_score=0 and matches only via tier-3 (substring).
	results, err = store.SearchLocalPaged(context.Background(), query, 10, 0, true)
	if err != nil {
		t.Fatalf("search with floor failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("with floor: expected 1 result (strong match only), got %d", len(results))
	}
	if len(results) > 0 && results[0].ID != 1 {
		t.Errorf("with floor: expected result id=1, got id=%d", results[0].ID)
	}

	// Now update the weak match to have popularity > 0
	database.Exec(`UPDATE games SET popularity_score = 100 WHERE id = 2`)

	// With floor and popularity > 0: both should be returned
	results, err = store.SearchLocalPaged(context.Background(), query, 10, 0, true)
	if err != nil {
		t.Fatalf("search with floor (after popularity update) failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("with floor and popularity > 0: expected 2 results, got %d", len(results))
	}
}

func TestSearchLocalPagedOffsetPagination(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// Insert 5 games that match "zelda"
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("Zelda %d", i)
		normalized := fmt.Sprintf("zelda %d", i)
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
			i, name, fmt.Sprintf("zelda-%d", i), normalized)
	}

	query := "zelda"

	// Page 1: offset=0, limit=2
	page1, err := store.SearchLocalPaged(context.Background(), query, 2, 0, false)
	if err != nil {
		t.Fatalf("page 1 search failed: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page 1: expected 2 results, got %d", len(page1))
	}

	// Page 2: offset=2, limit=2
	page2, err := store.SearchLocalPaged(context.Background(), query, 2, 2, false)
	if err != nil {
		t.Fatalf("page 2 search failed: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2: expected 2 results, got %d", len(page2))
	}

	// Page 3: offset=4, limit=2
	page3, err := store.SearchLocalPaged(context.Background(), query, 2, 4, false)
	if err != nil {
		t.Fatalf("page 3 search failed: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page 3: expected 1 result, got %d", len(page3))
	}

	// Verify pages are disjoint (no ID overlap)
	page1IDs := make(map[int64]bool)
	for _, r := range page1 {
		page1IDs[r.ID] = true
	}
	for _, r := range page2 {
		if page1IDs[r.ID] {
			t.Errorf("page 2 result %d overlaps with page 1", r.ID)
		}
	}

	// Verify pages are in order (first page should have lower IDs)
	if len(page1) > 0 && len(page2) > 0 && page1[0].ID > page2[0].ID {
		t.Errorf("pages not in order: page1[0].ID=%d > page2[0].ID=%d", page1[0].ID, page2[0].ID)
	}
}
