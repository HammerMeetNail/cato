package http

import (
	"context"
	"testing"
	"time"

	"cato/internal/config"
)

func TestNoopIGDBClient(t *testing.T) {
	// The noop client is what production uses when IGDB is unconfigured;
	// every method must return empty results without an error so search
	// falls back to local results.
	c := &noopIGDBClient{}
	found, err := c.SearchGames(context.Background(), "zelda", 10, false)
	if err != nil || len(found) != 0 {
		t.Errorf("SearchGames: got %v, %v", found, err)
	}
	game, err := c.GetGame(context.Background(), 1)
	if err != nil || game != nil {
		t.Errorf("GetGame: got %v, %v", game, err)
	}
	batch, err := c.GetGamesBatch(context.Background(), []int64{1, 2})
	if err != nil || len(batch) != 0 {
		t.Errorf("GetGamesBatch: got %v, %v", batch, err)
	}
	plats, err := c.GetPlatforms(context.Background())
	if err != nil || len(plats) != 0 {
		t.Errorf("GetPlatforms: got %v, %v", plats, err)
	}
}

func TestCatalogJobsWithoutIGDB(t *testing.T) {
	// Without IGDB config only the normalization repair is scheduled; the
	// IGDB-backed jobs stay off (no network calls).
	database := setupGamesTestDB(t)
	defer database.Close()
	handler := newCatalogWiring(database, &config.Config{})
	var names []string
	for _, j := range handler.Jobs(&config.Config{}) {
		names = append(names, j.Name)
	}
	if len(names) != 1 || names[0] != "catalog.normalization-repair" {
		t.Errorf("jobs without IGDB = %v", names)
	}
	with := handler.Jobs(&config.Config{IGDBClientID: "id"})
	if len(with) < 4 {
		t.Errorf("IGDB jobs missing: %d", len(with))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handler.service.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
