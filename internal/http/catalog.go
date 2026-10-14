package http

import (
	"context"
	"time"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/integrations/igdb"
	"cato/internal/jobs"
)

// catalogWiring owns the catalog service and its IGDB client; /api/v1
// serves the data and the job runner runs its recurring tasks.
type catalogWiring struct {
	igdb          catalog.IGDBClient
	service       *catalog.Service
	store         *catalog.Store
	searchLimiter *auth.RateLimiter
}

// defaultSearchRateLimit is the per-IP catalog searches per minute. The
// typeahead sends one request per debounced keystroke, so this leaves ample
// room for people while bounding scripted use.
const defaultSearchRateLimit = 120

func newCatalogWiring(database *db.DB, cfg *config.Config) *catalogWiring {
	var client catalog.IGDBClient = noopIGDBClient{}
	if cfg.IGDBClientID != "" {
		client = igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
	}
	return newCatalogWiringWithClient(database, cfg, client)
}

func newCatalogWiringWithClient(database *db.DB, cfg *config.Config, client catalog.IGDBClient) *catalogWiring {
	store := catalog.NewStore(database)
	limit := defaultSearchRateLimit
	if cfg.SearchRateLimit > 0 {
		limit = cfg.SearchRateLimit
	}
	return &catalogWiring{
		igdb:          client,
		service:       catalog.NewService(store, client, database),
		store:         store,
		searchLimiter: auth.NewRateLimiter(limit, time.Minute),
	}
}

// LaneStatus reports the IGDB limiter's lanes (empty without IGDB).
func (c *catalogWiring) LaneStatus() []catalog.LaneStatus {
	if l, ok := c.igdb.(interface{ LaneStatus() []catalog.LaneStatus }); ok {
		return l.LaneStatus()
	}
	return nil
}

// Jobs are the catalog's recurring tasks for the shared job runner.
func (c *catalogWiring) Jobs(cfg *config.Config) []jobs.Job {
	return c.service.Jobs(cfg.IGDBClientID != "")
}

// noopIGDBClient stands in when IGDB isn't configured: everything is local.
type noopIGDBClient struct{}

func (noopIGDBClient) SearchGames(context.Context, string, int, bool) ([]catalog.Game, error) {
	return nil, nil
}
func (noopIGDBClient) GetGame(context.Context, int64) (*catalog.Game, error) { return nil, nil }
func (noopIGDBClient) GetGamesBatch(context.Context, []int64) ([]catalog.Game, error) {
	return nil, nil
}
func (noopIGDBClient) GetPlatforms(context.Context) ([]catalog.Platform, error) { return nil, nil }
