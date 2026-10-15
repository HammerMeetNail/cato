package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
	"cato/internal/http"
	"cato/internal/importer"
	"cato/internal/integrations/igdb"
	"cato/internal/integrations/wikidata"
	"cato/internal/jobs"
	"cato/internal/library"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cato: %v\n", err)
		os.Exit(1)
	}
}

const usage = `usage: cato [command]

  serve (default)          run the web app
  dev-cert --out DIR       write a self-signed development certificate, print its SPKI pin
  import-games --input F   import a Postgres COPY dump of games [--db PATH]
  backfill KIND            refresh catalog rows from IGDB: aliases, popularity, covers,
                           editions or categories [--db PATH] [--batch N] [--recent-years N]
`

// run is the whole program behind main, so subcommands are testable.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	// Legacy names: backfill-aliases → backfill aliases.
	if kind, ok := strings.CutPrefix(cmd, "backfill-"); ok {
		cmd, args = "backfill", append([]string{kind}, args...)
	}
	switch cmd {
	case "serve":
		return serve(ctx, config.Load(), stderr)
	case "dev-cert":
		return devCertCommand(args, stdout, stderr)
	case "import-games":
		return importCommand(args, stdout, stderr)
	case "backfill":
		return backfillCommand(ctx, args, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func importCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("import-games", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "Postgres COPY dump SQL file")
	dbPath := fs.String("db", "data/cato.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("usage: cato import-games --input /tmp/games-copy.sql [--db data/cato.db]")
	}
	count, err := importer.Import(*input, *dbPath)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	fmt.Fprintf(stdout, "imported %d games\n", count)
	return nil
}

func backfillCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: cato backfill aliases|popularity|covers|editions|categories [--db PATH] [--batch N]")
	}
	kind := args[0]
	switch kind {
	case "aliases", "popularity", "covers", "editions", "categories":
	default:
		return fmt.Errorf("unknown backfill %q (aliases, popularity, covers, editions, categories)", kind)
	}
	fs := flag.NewFlagSet("backfill "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/cato.db", "SQLite database path")
	batch := fs.Int("batch", 500, "game IDs per IGDB request (max 500)")
	recentYears := fs.Int("recent-years", 2, "popularity: also backfill games released within this many years")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg := config.Load()
	if cfg.IGDBClientID == "" {
		return fmt.Errorf("backfill %s requires IGDB_CLIENT_ID (or TWITCH_OAUTH_ID)", kind)
	}
	database, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	svc := catalog.NewService(catalog.NewStore(database), igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret), database)
	progress := func(done, total int) {
		if total == 0 {
			fmt.Fprintln(stdout, "backfill: no pending rows")
			return
		}
		slog.Info("backfill", "kind", kind, "done", done, "total", total,
			"percent", fmt.Sprintf("%.1f", 100*float64(done)/float64(total)))
	}
	var done int
	switch kind {
	case "aliases":
		done, err = svc.BackfillAliases(ctx, *batch, progress)
	case "popularity":
		done, err = svc.BackfillPopularity(ctx, *batch, *recentYears, progress)
	case "covers":
		done, err = svc.BackfillCovers(ctx, *batch, progress)
	case "editions":
		done, err = svc.BackfillEditions(ctx, *batch, progress)
	case "categories":
		done, err = svc.BackfillCategories(ctx, *batch, progress)
	}
	if err != nil {
		return fmt.Errorf("backfill %s stopped after %d rows (safe to re-run): %w", kind, done, err)
	}
	fmt.Fprintf(stdout, "backfill %s: %d games\n", kind, done)
	return nil
}

// newLogger configures structured logging. slog.SetDefault also routes the
// standard log package through it, so older log.Printf calls share the format.
func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(w, opts)
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func serve(ctx context.Context, cfg *config.Config, stderr io.Writer) error {
	logger := newLogger(cfg, stderr)
	slog.SetDefault(logger)
	if err := prepareTLS(cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(filepath.Dir(cfg.DBPath), "backups")
	}
	logger.Info("cato starting", cfg.Summary()...)

	if err := os.MkdirAll(cfg.CoverDir, 0o755); err != nil {
		return fmt.Errorf("create cover dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	from, _ := db.SchemaVersion(database)
	if path, err := db.BackupBeforeMigrate(ctx, database, cfg.BackupDir, 5); err != nil {
		return fmt.Errorf("pre-migration backup: %w", err)
	} else if path != "" {
		logger.Info("snapshot taken before schema upgrade", "path", path, "from_version", from, "to_version", db.LatestVersion())
	}
	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	startupRepairs(ctx, database, logger)

	// Bind before starting background owners: a bad certificate or a busy
	// port fails startup with nothing to unwind.
	srv := http.NewServer(cfg, database)
	if err := srv.Listen(); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	coverWorker := covers.NewWorker(database, cfg.CoverDir)
	srv.SetCoverNotifier(coverWorker.Wake)
	coverWorker.Start()

	runner := jobs.NewRunner(database, logger)
	for _, j := range srv.Jobs() {
		runner.Add(j)
	}
	runner.Add(jobs.Job{Name: "maintenance", Interval: 24 * time.Hour, Jitter: 30 * time.Minute,
		Run: func(ctx context.Context) error { return maintenance(ctx, database, logger) }})
	if cfg.WikidataLinks {
		runner.Add(jobs.Job{Name: "wikidata.crosswalk", Interval: 7 * 24 * time.Hour, Jitter: 6 * time.Hour, FirstRun: 20 * time.Minute,
			Run: wikidata.Crosswalk(database, wikidata.New(), 20)})
	}
	srv.SetJobRunner(runner)
	runner.Start(context.Background())
	// Warm Discover's catalog shelves now: the persisted schedule would
	// otherwise leave the first visitor after a restart to compute them.
	runner.RunNow("discover.refresh")

	scheme := "http"
	if cfg.ServesTLS() {
		scheme = "https"
	}
	logger.Info("cato listening", "url", fmt.Sprintf("%s://%s", scheme, srv.Addr()))
	if a := srv.RedirectAddr(); a != nil {
		logger.Info("redirecting http to https", "addr", a.String())
	}

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.Start() }()

	var failed error
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			failed = fmt.Errorf("server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	// Drain admitted HTTP requests first, then stop background owners,
	// leaving five seconds of the budget for them.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	drainCtx, cancelDrain := context.WithTimeout(shutdownCtx, 15*time.Second)
	shutdownErr := srv.Shutdown(drainCtx)
	cancelDrain()
	jobsErr := runner.Shutdown(shutdownCtx)
	coverErr := coverWorker.Shutdown(shutdownCtx)
	if err := errors.Join(shutdownErr, jobsErr, coverErr); err != nil {
		// Don't close the database while an owner may still use it.
		return errors.Join(failed, fmt.Errorf("shutdown: %w", err))
	}
	optimizeCtx, cancelOptimize := context.WithTimeout(context.Background(), 5*time.Second)
	if err := database.Optimize(optimizeCtx); err != nil {
		logger.Warn("shutdown: optimize failed", "err", err)
	}
	cancelOptimize()
	return failed
}

// startupRepairs runs cheap, idempotent data fixes once per start.
func startupRepairs(ctx context.Context, database *db.DB, logger *slog.Logger) {
	if n, err := catalog.PurgeDeadCoverSources(database); err != nil {
		logger.Warn("startup: dead cover source purge failed", "err", err)
	} else if n > 0 {
		logger.Info("startup: cleared games pointing at dead cover host", "count", n)
	}
	// Ownership once encoded as tags ("Switch", "PS5") moves into the
	// platform list; no-ops once every mappable tag is gone.
	if n, err := catalog.MigratePlatformTags(database); err != nil {
		logger.Warn("startup: platform tag migration failed", "err", err)
	} else if n > 0 {
		logger.Info("startup: migrated platform tags to ownership", "count", n)
	}
	if n, err := catalog.NewStore(database).RepairTagCaseDuplicates(ctx); err != nil {
		logger.Warn("startup: tag case dedupe failed", "err", err)
	} else if n > 0 {
		logger.Info("startup: deduplicated tags", "count", n)
	}
}

// maintenance is the daily sweep: expired sessions, IGDB cache entries,
// orphaned cover jobs and undo snapshots, then planner statistics.
func maintenance(ctx context.Context, database *db.DB, logger *slog.Logger) error {
	var errs []error
	step := func(name string, n int64, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		} else if n > 0 {
			logger.Info("maintenance", "step", name, "removed", n)
		}
	}
	n, err := auth.CleanupExpiredSessions(maintenanceDB{DB: database, ctx: ctx})
	step("sessions", n, err)
	n, err = catalog.PurgeExpiredQueryCacheContext(ctx, database)
	step("query-cache", n, err)
	n, err = covers.PruneJobs(ctx, database)
	step("cover-jobs", n, err)
	n, err = library.PruneUndo(ctx, database)
	step("undo-log", n, err)
	if err := database.Optimize(ctx); err != nil {
		errs = append(errs, fmt.Errorf("optimize: %w", err))
	}
	return errors.Join(errs...)
}

// maintenanceDB binds the auth cleanup's Exec to the sweep's context.
type maintenanceDB struct {
	*db.DB
	ctx context.Context
}

func (d maintenanceDB) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(d.ctx, query, args...)
}
