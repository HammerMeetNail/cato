package catalog

import (
	"context"
	"log"
	"time"

	"cato/internal/jobs"
)

// Jobs are the catalog's recurring tasks for the shared runner. The
// IGDB-backed ones are only registered when a real client is configured.
func (s *Service) Jobs(igdbConfigured bool) []jobs.Job {
	list := []jobs.Job{{
		// Legacy accent-bearing normalized names (cheap, idempotent).
		Name: "catalog.normalization-repair", Interval: 24 * time.Hour, Jitter: time.Hour,
		Run: func(ctx context.Context) error {
			_, err := s.RepairNormalization(ctx)
			return err
		},
	}}
	if !igdbConfigured {
		return list
	}
	return append(list,
		jobs.Job{
			// One request; a no-op once the table is filled (shortnames are
			// re-applied every run).
			Name: "catalog.platform-sync", Interval: 24 * time.Hour, Jitter: time.Hour,
			Run: s.SyncPlatforms,
		},
		jobs.Job{
			Name: "catalog.stale-refresh", Interval: 6 * time.Hour, Jitter: 30 * time.Minute, FirstRun: 5 * time.Minute,
			Run: func(ctx context.Context) error { s.refreshStaleGamesContext(ctx, 100); return nil },
		},
		jobs.Job{
			// Keeps popular queries fresh so the first search after expiry
			// still hits a warm cache.
			Name: "catalog.query-cache-refresh", Interval: 24 * time.Hour, Jitter: time.Hour, FirstRun: 30 * time.Second,
			Run: func(ctx context.Context) error { s.refreshStaleQueriesContext(ctx, 50); return nil },
		},
		jobs.Job{
			Name: "catalog.cover-repair", Interval: 7 * 24 * time.Hour, Jitter: 6 * time.Hour, FirstRun: 2 * time.Minute,
			Run: func(ctx context.Context) error { s.RepairCovers(ctx); return nil },
		},
	)
}

// jobNamed returns one of s.Jobs(true) (tests).
func (s *Service) jobNamed(name string) jobs.Job {
	for _, j := range s.Jobs(true) {
		if j.Name == name {
			return j
		}
	}
	log.Panicf("no job %s", name)
	return jobs.Job{}
}
