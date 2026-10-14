package catalog

import (
	"context"
	"testing"
	"time"
)

// F4: search and refresh results must not queue cover jobs for games that are
// in nobody's library (the worker never downloads those; the table only grew).
func TestCoverJobsOnlyForLibraryGames(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Owned', 'owned', 'owned'), (2, 'Catalog', 'catalog', 'catalog')`)
	putInLibrary(t, database, 1)
	for _, id := range []int64{1, 2} {
		if err := store.EnqueueCoverJob(ctx, id, "https://images.igdb.com/x.jpg"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 2`).Scan(&n)
	if n != 0 {
		t.Fatal("cover job queued for a game in no library")
	}
	database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 1`).Scan(&n)
	if n != 1 {
		t.Fatal("library game lost its cover job")
	}

	// A search refresh stores the remote games but queues no cover jobs.
	igdb := &fakeIGDB{searchFunc: func(ctx context.Context, q string, limit int) ([]Game, error) {
		return []Game{{ID: 50, Name: "Remote", Slug: "remote", NormalizedName: "remote", CoverURL: "https://images.igdb.com/r.jpg"}}, nil
	}}
	svc := NewService(store, igdb, database)
	defer shutdownService(t, svc)
	if _, err := svc.Search(ctx, "remote", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		var c int
		database.QueryRow(`SELECT COUNT(*) FROM games WHERE id = 50`).Scan(&c)
		return c == 1
	}, "refresh did not store the remote game")
	database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 50`).Scan(&n)
	if n != 0 {
		t.Fatal("search refresh queued a cover job")
	}
}
