package games

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackfillAliases(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'The Legend of Zelda: Breath of the Wild', 'a', 'the legend of zelda breath of the wild'),
		(2, 'Cult of the Lamb', 'b', 'cult of the lamb'),
		(3, 'Doom', 'c', 'doom'),
		(4, 'Deleted Upstream', 'd', 'deleted upstream')`)

	igdb := &fakeIGDB{
		batchFunc: func(ctx context.Context, ids []int64) ([]Game, error) {
			return []Game{
				{ID: 1, Name: "The Legend of Zelda: Breath of the Wild",
					Aliases: []string{"BotW", "Breath of the Wild"}},
				{ID: 2, Name: "Cult of the Lamb"}, // no aliases upstream
			}, nil
		},
	}
	svc := NewService(store, igdb, database)

	var total int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE aliases_fetched_at = 0`).Scan(&total)
	if total != 4 {
		t.Fatalf("expected 4 pending, got %d", total)
	}

	progressed := []int{}
	done, err := svc.BackfillAliases(ctx, 500, func(d, tot int) { progressed = append(progressed, d) })
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if done != 4 {
		t.Errorf("expected 4 processed, got %d", done)
	}

	database.QueryRow(`SELECT COUNT(*) FROM games WHERE aliases_fetched_at = 0`).Scan(&total)
	if total != 0 {
		t.Errorf("all rows should be marked fetched, %d pending", total)
	}

	// Game 1: aliases written and searchable.
	results, err := store.SearchLocal(ctx, "botw", 10)
	if err != nil || len(results) != 1 || results[0].ID != 1 {
		t.Errorf("alias search after backfill failed: %v (%v)", results, err)
	}

	// Game 2: zero-alias set is authoritative (no rows), but marked.
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("game 2 should have no aliases, got %d", n)
	}
	var fetched int64
	database.QueryRow(`SELECT aliases_fetched_at > 0 FROM games WHERE id = 2`).Scan(&fetched)
	if fetched == 0 {
		t.Error("game 2 should be marked fetched")
	}

	// Game 4: missing from IGDB response — stamped anyway so the queue advances.
	database.QueryRow(`SELECT aliases_fetched_at > 0 FROM games WHERE id = 4`).Scan(&fetched)
	if fetched == 0 {
		t.Error("missing-from-response game 4 should be marked fetched")
	}

	// Resumable: re-running finds nothing pending and issues no requests.
	callsBefore := igdb.batchCalls
	done2, err := svc.BackfillAliases(ctx, 500, func(d, tot int) {})
	if err != nil || done2 != 0 {
		t.Errorf("re-run should process nothing, got done=%d err=%v", done2, err)
	}
	if igdb.batchCalls != callsBefore {
		t.Errorf("re-run should not call IGDB, calls went %d -> %d", callsBefore, igdb.batchCalls)
	}
}

func TestBackfillAliasesBatchingAndChunking(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	const n = 25
	var sb strings.Builder
	sb.WriteString(`INSERT INTO games (id, name, slug, normalized_name) VALUES `)
	for i := 1; i <= n; i++ {
		if i > 1 {
			sb.WriteString(",")
		}
		sb.WriteString("(" + strconv.Itoa(i) + ", 'Game " + strconv.Itoa(i) + "', 'g" + strconv.Itoa(i) + "', 'game " + strconv.Itoa(i) + "')")
	}
	database.Exec(sb.String())

	sizes := []int{}
	igdb := &fakeIGDB{
		batchFunc: func(ctx context.Context, ids []int64) ([]Game, error) {
			sizes = append(sizes, len(ids))
			out := make([]Game, 0, len(ids))
			for _, id := range ids {
				out = append(out, Game{ID: id, Name: "Game " + strconv.Itoa(int(id)), Aliases: []string{"nick" + strconv.Itoa(int(id))}})
			}
			return out, nil
		},
	}
	svc := NewService(store, igdb, database)

	done, err := svc.BackfillAliases(ctx, 10, func(d, tot int) {})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if done != n {
		t.Errorf("expected %d processed, got %d", n, done)
	}
	// 25 rows at batch size 10 → three chunks: 10/10/5, all ≤ 500.
	if len(sizes) != 3 || sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		t.Errorf("expected chunk sizes [10 10 5], got %v", sizes)
	}

	results, _ := store.SearchLocal(ctx, "nick7", 10)
	if len(results) != 1 || results[0].ID != 7 {
		t.Errorf("backfilled alias nick7 should find game 7; got %v", results)
	}
}

// TestBackfillRetriesThrottledBatches: a transient IGDB failure (e.g. a 429
// from another process sharing the API key) must be retried with backoff,
// not abort the run.
func TestBackfillRetriesThrottledBatches(t *testing.T) {
	oldDelays := backfillRetryDelays
	backfillRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { backfillRetryDelays = oldDelays }()

	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'Game One', 'g1', 'game one')`)

	attempts := 0
	igdb := &fakeIGDB{
		batchFunc: func(ctx context.Context, ids []int64) ([]Game, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("igdb rate limited (429)")
			}
			return []Game{{ID: 1, Name: "Game One", Aliases: []string{"g1"}}}, nil
		},
	}
	svc := NewService(store, igdb, database)

	done, err := svc.BackfillAliases(ctx, 500, func(d, tot int) {})
	if err != nil {
		t.Fatalf("backfill should ride out transient failures: %v", err)
	}
	if done != 1 || attempts != 3 {
		t.Errorf("expected done=1 after 3 attempts, got done=%d attempts=%d", done, attempts)
	}

	results, _ := store.SearchLocal(ctx, "g1", 10)
	if len(results) != 1 || results[0].ID != 1 {
		t.Errorf("alias should be searchable after retried run; got %v", results)
	}
}

// TestBackfillStopsAfterExhaustedRetries verifies the bounded-retry contract:
// persistent failure returns an error (progress saved) instead of hanging.
func TestBackfillStopsAfterExhaustedRetries(t *testing.T) {
	oldDelays := backfillRetryDelays
	backfillRetryDelays = []time.Duration{time.Millisecond}
	defer func() { backfillRetryDelays = oldDelays }()

	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'G', 'g', 'g')`)

	attempts := 0
	igdb := &fakeIGDB{
		batchFunc: func(ctx context.Context, ids []int64) ([]Game, error) {
			attempts++
			return nil, errors.New("igdb rate limited (429)")
		},
	}
	svc := NewService(store, igdb, database)

	done, err := svc.BackfillAliases(ctx, 500, func(d, tot int) {})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	// 1 initial attempt + len(backfillRetryDelays) retries.
	if attempts != 1+len(backfillRetryDelays) {
		t.Errorf("expected %d attempts, got %d", 1+len(backfillRetryDelays), attempts)
	}
	var marked int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE aliases_fetched_at > 0`).Scan(&marked)
	if marked != 0 || done != 0 {
		t.Errorf("failed batch must not stamp progress; marked=%d done=%d", marked, done)
	}
}

// alternative_names, so it must stamp the marker too.
// TestUpsertMarksAliasesFetched: every full IGDB upsert now carries
// alternative_names, so it must stamp the marker too.
func TestUpsertMarksAliasesFetched(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	err := store.UpsertIGDBGame(context.Background(), Game{
		ID: 42, Name: "Some Game", Slug: "sg", SafeName: "x",
		NormalizedName: NormalizeName("Some Game"),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var fetched int64
	database.QueryRow(`SELECT aliases_fetched_at > 0 FROM games WHERE id = 42`).Scan(&fetched)
	if fetched == 0 {
		t.Error("upsert should stamp aliases_fetched_at")
	}

	candidates, err := store.GetAliasBackfillCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 0 {
		t.Errorf("marked game must not be a candidate; got %v (%v)", candidates, err)
	}
}
