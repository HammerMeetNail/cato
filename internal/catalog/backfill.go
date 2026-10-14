package catalog

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// backfillSpec describes one resumable IGDB backfill. They all share one
// loop (runBackfill): count the pending rows, then fetch candidate IDs in
// batches, ask IGDB about them, apply what came back and mark what didn't
// (deleted upstream) so the queue always advances.
type backfillSpec struct {
	name  string
	count func(ctx context.Context) (int64, error)
	// next returns up to limit candidate IDs. after is the last ID of the
	// previous batch, for specs that page by ID rather than by a marker.
	next func(ctx context.Context, after int64, limit int) ([]int64, error)
	// fetch defaults to one batched request (with retries).
	fetch func(ctx context.Context, ids []int64) ([]Game, error)
	// apply handles a game IGDB returned; missing one it didn't (nil: skip).
	apply   func(ctx context.Context, g Game) error
	missing func(ctx context.Context, id int64) error
	// failFast stops the run when a batch fetch fails after retries (the
	// marker makes re-running cheap); otherwise the batch is skipped.
	failFast bool
}

// errSkip from missing leaves an ID pending without counting or logging it.
var errSkip = errors.New("skip")

func clampBatch(n int) int {
	if n <= 0 || n > 500 {
		return 500
	}
	return n
}

// runBackfill drives spec to completion or cancellation, reporting
// (done, total) after every batch.
func (s *Service) runBackfill(ctx context.Context, spec backfillSpec, batchSize int, progress func(done, total int)) (int, error) {
	batchSize = clampBatch(batchSize)
	if spec.fetch == nil {
		spec.fetch = s.fetchBatchWithRetry
	}
	pending, err := spec.count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	total, done := int(pending), 0
	progress(done, total)
	if total == 0 {
		return 0, nil
	}
	var after int64
	for {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ids, err := spec.next(ctx, after, batchSize)
		if err != nil {
			return done, fmt.Errorf("get candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		games, err := spec.fetch(ctx, ids)
		if err != nil {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			if spec.failFast {
				return done, fmt.Errorf("batch fetch at %d/%d: %w", done, total, err)
			}
			log.Printf("%s: batch of %d failed: %v", spec.name, len(ids), err)
			continue
		}
		byID := make(map[int64]Game, len(games))
		for _, g := range games {
			byID[g.ID] = g
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			g, ok := byID[id]
			if !ok {
				if spec.missing != nil {
					if err := spec.missing(ctx, id); err != nil {
						if err != errSkip {
							log.Printf("%s: mark %d failed: %v", spec.name, id, err)
						}
						continue
					}
				}
				done++
				continue
			}
			if err := spec.apply(ctx, g); err != nil {
				log.Printf("%s: game %d failed: %v", spec.name, id, err)
				continue
			}
			done++
		}
		progress(done, total)
	}
	return done, nil
}

// BackfillCovers re-fetches every game whose cover_url looks guessed
// (co<digits>) and corrects the URL from cover.image_id. Pages by ID.
func (s *Service) BackfillCovers(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	return s.runBackfill(ctx, backfillSpec{
		name:     "backfill-covers",
		count:    s.store.CountPendingCoverRepair,
		next:     s.store.GetCoverRepairCandidatesAfter,
		failFast: true,
		apply: func(ctx context.Context, g Game) error {
			var storedURL string
			var storedCoverID int64
			_ = s.db.QueryRowContext(ctx, `SELECT cover_url, cover_id FROM games WHERE id = ?`, g.ID).Scan(&storedURL, &storedCoverID)
			if g.CoverURL == storedURL && g.CoverID == storedCoverID {
				return nil
			}
			if err := s.store.SetCoverAndMarkFetched(ctx, g.ID, g.CoverID, g.CoverURL); err != nil {
				return err
			}
			if g.CoverURL != "" {
				s.store.EnqueueCoverJob(ctx, g.ID, g.CoverURL)
			}
			return nil
		},
	}, batchSize, progress)
}

// BackfillPopularity re-fetches rows missing popularity data (full game
// records, one request per game). Resumable via popularity_fetched_at.
func (s *Service) BackfillPopularity(ctx context.Context, batchSize, recentYears int, progress func(done, total int)) (int, error) {
	// A transient per-game failure leaves the row pending (unlike a game IGDB
	// no longer knows, which is marked so the queue advances).
	failed := map[int64]bool{}
	return s.runBackfill(ctx, backfillSpec{
		name:  "backfill-popularity",
		count: func(ctx context.Context) (int64, error) { return s.store.CountPendingBackfill(ctx, recentYears) },
		next: func(ctx context.Context, _ int64, limit int) ([]int64, error) {
			return s.store.GetBackfillCandidates(ctx, limit, recentYears)
		},
		fetch: func(ctx context.Context, ids []int64) ([]Game, error) {
			var out []Game
			for _, id := range ids {
				g, err := s.igdb.GetGame(ctx, id)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					log.Printf("backfill-popularity: game %d failed: %v", id, err)
					failed[id] = true
					continue
				}
				if g != nil {
					out = append(out, *g)
				}
			}
			return out, nil
		},
		apply: func(ctx context.Context, g Game) error { return s.store.UpsertIGDBGame(ctx, g) },
		missing: func(ctx context.Context, id int64) error {
			if failed[id] {
				delete(failed, id)
				return errSkip
			}
			return s.store.MarkPopularityFetched(ctx, id)
		},
	}, batchSize, progress)
}

// BackfillAliases populates game_aliases in bulk. Resumable via
// aliases_fetched_at; IDs IGDB no longer knows are stamped too.
func (s *Service) BackfillAliases(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	return s.runBackfill(ctx, backfillSpec{
		name:  "backfill-aliases",
		count: s.store.CountPendingAliasBackfill,
		next: func(ctx context.Context, _ int64, limit int) ([]int64, error) {
			return s.store.GetAliasBackfillCandidates(ctx, limit)
		},
		failFast: true,
		apply: func(ctx context.Context, g Game) error {
			return s.store.SetAliasesAndMarkFetched(ctx, g.ID, g.Name, g.Aliases)
		},
		missing: s.store.MarkAliasesFetched,
	}, batchSize, progress)
}

// BackfillEditions populates version_parent in bulk. Resumable via
// version_parent_fetched_at.
func (s *Service) BackfillEditions(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	return s.runBackfill(ctx, backfillSpec{
		name:  "backfill-editions",
		count: s.store.CountPendingEditionBackfill,
		next: func(ctx context.Context, _ int64, limit int) ([]int64, error) {
			return s.store.GetEditionBackfillCandidates(ctx, limit)
		},
		failFast: true,
		apply: func(ctx context.Context, g Game) error {
			return s.store.SetEditionInfoAndMarkFetched(ctx, g.ID, g.VersionParent, g.Category, g.ParentGame)
		},
		missing: s.store.MarkEditionFetched,
	}, batchSize, progress)
}

// BackfillCategories walks the whole catalog by ID and corrects stale
// category/parent_game (and missed version_parent) values.
func (s *Service) BackfillCategories(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	return s.runBackfill(ctx, backfillSpec{
		name:     "backfill-categories",
		count:    s.store.CountTotalGames,
		next:     s.store.GetGameIDsAfter,
		failFast: true,
		apply: func(ctx context.Context, g Game) error {
			if err := s.store.UpdateCategoryAndParentGame(ctx, g.ID, g.Category, g.ParentGame); err != nil {
				return err
			}
			if g.VersionParent != 0 {
				_ = s.store.SetVersionParentAndMarkFetched(ctx, g.ID, g.VersionParent)
			}
			return nil
		},
	}, batchSize, progress)
}
