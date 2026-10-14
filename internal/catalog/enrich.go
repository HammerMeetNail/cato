package catalog

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"cato/internal/jobs"
)

// DetailsMaxAge is how old a game's rich data may get before a game page
// view refreshes it (plan §6.1: on demand, when older than N days).
const DetailsMaxAge = 14 * 24 * time.Hour

func (s *Service) details() (DetailsClient, bool) {
	d, ok := s.igdb.(DetailsClient)
	return d, ok
}

// RefreshDetails fetches rich data and time to beat for up to 500 games.
func (s *Service) RefreshDetails(ctx context.Context, ids []int64) error {
	dc, ok := s.details()
	if !ok || len(ids) == 0 {
		return nil
	}
	ds, err := dc.GetDetails(ctx, ids)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, d := range ds {
		if err := s.store.SaveDetails(ctx, d, now); err != nil {
			return err
		}
	}
	// IDs IGDB didn't return still count as fetched, so they aren't retried
	// on every page view.
	returned := map[int64]bool{}
	for _, d := range ds {
		returned[d.GameID] = true
	}
	for _, id := range ids {
		if !returned[id] {
			s.db.ExecContext(ctx, `UPDATE games SET details_fetched_at = ? WHERE id = ?`, now.Unix(), id)
		}
	}
	ttb, err := dc.GetTimesToBeat(ctx, ids)
	if err != nil {
		return err
	}
	return s.store.SaveTimesToBeat(ctx, ttb)
}

// EnsureDetails refreshes a game's rich data when it is missing or older
// than DetailsMaxAge. A game that never had details is fetched before
// returning (bounded by wait); stale ones refresh in the background.
func (s *Service) EnsureDetails(ctx context.Context, id int64, wait time.Duration) {
	if _, ok := s.details(); !ok || id <= 0 {
		return
	}
	fetched, err := s.store.DetailsAge(ctx, id)
	if err != nil || (!fetched.IsZero() && time.Since(fetched) < DetailsMaxAge) {
		return
	}
	run := func(ctx context.Context) {
		ctx = WithLane(ctx, LaneInteractive)
		if err := s.RefreshDetails(ctx, []int64{id}); err != nil && ctx.Err() == nil {
			slog.Warn("game details refresh failed", "game", id, "err", err)
		}
	}
	if fetched.IsZero() {
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		run(wctx)
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		bg, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		run(bg)
	}()
}

// RefreshLibraryDetails is the weekly job: every library game whose rich
// data is over a week old, in batches of 500.
func (s *Service) RefreshLibraryDetails(ctx context.Context) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for ctx.Err() == nil {
		ids, err := s.store.StaleDetails(ctx, cutoff, maxBatch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if dc, ok := s.details(); ok {
			if games, err := dc.GetGamesFull(ctx, ids); err == nil {
				for _, g := range games {
					s.store.UpsertIGDBGame(ctx, g)
				}
			}
		}
		if err := s.RefreshDetails(ctx, ids); err != nil {
			return err
		}
	}
	return ctx.Err()
}

const maxBatch = 500

// deltaPagesPerRun bounds one delta sync run (IGDB changes a few thousand
// games a day).
const deltaPagesPerRun = 20

// DeltaSync upserts games IGDB changed since the last run (replacing the
// refresh-by-age loop). The first run starts a day back.
func (s *Service) DeltaSync(ctx context.Context) error {
	dc, ok := s.details()
	if !ok {
		return nil
	}
	raw, err := s.store.syncState(ctx, "delta_since")
	if err != nil {
		return err
	}
	since, _ := strconv.ParseInt(raw, 10, 64)
	if since == 0 {
		since = time.Now().Add(-24 * time.Hour).Unix()
	}
	newest := since
	for page := 0; page < deltaPagesPerRun && ctx.Err() == nil; page++ {
		games, err := dc.GetUpdatedSince(ctx, since, page*maxBatch, maxBatch)
		if err != nil {
			return err
		}
		for _, g := range games {
			if err := s.store.UpsertIGDBGame(ctx, g); err != nil {
				return err
			}
			if g.CoverURL != "" {
				s.store.EnqueueCoverJob(ctx, g.ID, g.CoverURL)
			}
			newest = max(newest, g.SourceUpdatedAt)
		}
		if len(games) < maxBatch {
			break
		}
	}
	return s.store.setSyncState(ctx, "delta_since", strconv.FormatInt(newest, 10))
}

// SyncLookups refreshes the genre/theme/mode/perspective/game type names.
func (s *Service) SyncLookups(ctx context.Context) error {
	dc, ok := s.details()
	if !ok {
		return nil
	}
	for _, kind := range LookupKinds {
		refs, err := dc.GetLookup(ctx, kind)
		if err != nil {
			return err
		}
		if err := s.store.SaveLookups(ctx, kind, refs); err != nil {
			return err
		}
	}
	return nil
}

// SyncPopularity refreshes the Trending ranking (and the games it names
// that aren't in the catalog yet).
func (s *Service) SyncPopularity(ctx context.Context) error {
	dc, ok := s.details()
	if !ok {
		return nil
	}
	for _, t := range []int64{PopularityVisits, PopularityWantToPlay, PopularityPlaying} {
		list, err := dc.GetPopular(ctx, t, 200)
		if err != nil {
			return err
		}
		ids := make([]int64, len(list))
		for i, p := range list {
			ids[i] = p.GameID
		}
		missing, err := s.store.MissingGames(ctx, ids)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			games, err := dc.GetGamesFull(ctx, missing)
			if err != nil {
				return err
			}
			for _, g := range games {
				s.store.UpsertIGDBGame(ctx, g)
			}
		}
		if err := s.store.SavePopularity(ctx, t, list, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// richJobs are the §6.1 recurring tasks (IGDB configured only).
func (s *Service) richJobs() []jobs.Job {
	return []jobs.Job{
		{Name: "catalog.lookups", Interval: 7 * 24 * time.Hour, Jitter: time.Hour, Run: s.SyncLookups},
		{Name: "catalog.library-refresh", Interval: 7 * 24 * time.Hour, Jitter: 6 * time.Hour, FirstRun: 10 * time.Minute,
			Timeout: 2 * time.Hour, Run: s.RefreshLibraryDetails},
		{Name: "catalog.delta-sync", Interval: 6 * time.Hour, Jitter: 30 * time.Minute, FirstRun: 15 * time.Minute, Run: s.DeltaSync},
		{Name: "catalog.popularity", Interval: 24 * time.Hour, Jitter: time.Hour, FirstRun: 3 * time.Minute, Run: s.SyncPopularity},
	}
}

// EnsureGames stores catalog rows for IGDB games this server hasn't seen
// (an import matched them by Steam app ID), 500 per request. Without a
// details-capable IGDB client it does nothing.
func (s *Service) EnsureGames(ctx context.Context, ids []int64) error {
	dc, ok := s.details()
	if !ok || len(ids) == 0 {
		return nil
	}
	var missing []int64
	for _, id := range ids {
		var n int
		s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE id = ?`, id).Scan(&n)
		if n == 0 {
			missing = append(missing, id)
		}
	}
	for start := 0; start < len(missing); start += 500 {
		games, err := dc.GetGamesFull(WithLane(ctx, LaneInteractive), missing[start:min(start+500, len(missing))])
		if err != nil {
			return err
		}
		for _, g := range games {
			if err := s.store.UpsertIGDBGame(ctx, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// ExternalLookup maps other services' IDs to IGDB games (the IGDB client).
type ExternalLookup interface {
	GamesByExternal(ctx context.Context, source int, uids []string) (map[string]int64, error)
}

// External returns the IGDB client's external-ID lookup when it has one.
func (s *Service) External() (ExternalLookup, bool) {
	e, ok := s.igdb.(ExternalLookup)
	return e, ok
}
