package games

import (
	"context"
	"database/sql"
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

func setupGameDB(t *testing.T) (*sql.DB, *Store) {
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

func TestSearchDoesNotCallIGDBWhenLocalResultsAreEnough(t *testing.T) {
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
	if igdb.searchCalls != 0 {
		t.Errorf("expected no IGDB calls when local results >= 3, got %d", igdb.searchCalls)
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
		t.Errorf("expected 1 IGDB call when local results < 3, got %d", igdb.searchCalls)
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
