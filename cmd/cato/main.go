package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
	"cato/internal/games"
	"cato/internal/http"
	"cato/internal/importer"
	"cato/internal/igdb"
)

func main() {
	importCmd := flag.NewFlagSet("import-games", flag.ExitOnError)
	importInput := importCmd.String("input", "", "Postgres COPY dump SQL file")
	importDB := importCmd.String("db", "data/cato.db", "SQLite database path")

	backfillCmd := flag.NewFlagSet("backfill-popularity", flag.ExitOnError)
	backfillDB := backfillCmd.String("db", "data/cato.db", "SQLite database path")
	backfillBatch := backfillCmd.Int("batch", 500, "rows per IGDB fetch cycle")
	backfillYears := backfillCmd.Int("recent-years", 2, "also backfill games released within this many years")

	aliasBackfillCmd := flag.NewFlagSet("backfill-aliases", flag.ExitOnError)
	aliasBackfillDB := aliasBackfillCmd.String("db", "data/cato.db", "SQLite database path")
	aliasBackfillBatch := aliasBackfillCmd.Int("batch", 500, "game IDs per IGDB request (max 500)")

	coverBackfillCmd := flag.NewFlagSet("backfill-covers", flag.ExitOnError)
	coverBackfillDB := coverBackfillCmd.String("db", "data/cato.db", "SQLite database path")
	coverBackfillBatch := coverBackfillCmd.Int("batch", 500, "game IDs per IGDB request (max 500)")

	editionBackfillCmd := flag.NewFlagSet("backfill-editions", flag.ExitOnError)
	editionBackfillDB := editionBackfillCmd.String("db", "data/cato.db", "SQLite database path")
	editionBackfillBatch := editionBackfillCmd.Int("batch", 500, "game IDs per IGDB request (max 500)")

	if len(os.Args) >= 2 && os.Args[1] == "import-games" {
		importCmd.Parse(os.Args[2:])
		if *importInput == "" {
			fmt.Fprintln(os.Stderr, "usage: cato import-games --input /tmp/games-copy.sql [--db data/cato.db]")
			os.Exit(1)
		}
		count, err := importer.Import(*importInput, *importDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("imported %d games\n", count)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "backfill-aliases" {
		aliasBackfillCmd.Parse(os.Args[2:])
		cfg := config.Load()
		if cfg.IGDBClientID == "" {
			fmt.Fprintln(os.Stderr, "backfill-aliases requires IGDB_CLIENT_ID (or TWITCH_OAUTH_ID)")
			os.Exit(1)
		}
		database, err := db.Open(*aliasBackfillDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer database.Close()
		if err := db.Migrate(database); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
		store := games.NewStore(database)
		igdbClient := igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
		svc := games.NewService(store, igdbClient, database)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		progress := func(done, total int) {
			if total == 0 {
				fmt.Println("backfill: no pending rows")
				return
			}
			log.Printf("backfill: %d/%d (%.1f%%)", done, total, 100*float64(done)/float64(total))
		}
		done, err := svc.BackfillAliases(ctx, *aliasBackfillBatch, progress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill stopped: %v (completed %d — safe to re-run)\n", err, done)
			os.Exit(1)
		}
		fmt.Printf("backfill: fetched aliases for %d games\n", done)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "backfill-popularity" {
		backfillCmd.Parse(os.Args[2:])
		cfg := config.Load()
		if cfg.IGDBClientID == "" {
			fmt.Fprintln(os.Stderr, "backfill-popularity requires IGDB_CLIENT_ID (or TWITCH_OAUTH_ID)")
			os.Exit(1)
		}
		database, err := db.Open(*backfillDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer database.Close()
		if err := db.Migrate(database); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
		store := games.NewStore(database)
		igdbClient := igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
		svc := games.NewService(store, igdbClient, database)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		progress := func(done, total int) {
			if total == 0 {
				log.Printf("backfill: no pending rows")
				return
			}
			log.Printf("backfill: %d/%d (%.1f%%)", done, total, 100*float64(done)/float64(total))
		}
		done, err := svc.BackfillPopularity(ctx, *backfillBatch, *backfillYears, progress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill stopped: %v (completed %d)\n", err, done)
			os.Exit(1)
		}
		fmt.Printf("backfill: refreshed popularity for %d games\n", done)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "backfill-covers" {
		coverBackfillCmd.Parse(os.Args[2:])
		cfg := config.Load()
		if cfg.IGDBClientID == "" {
			fmt.Fprintln(os.Stderr, "backfill-covers requires IGDB_CLIENT_ID (or TWITCH_OAUTH_ID)")
			os.Exit(1)
		}
		database, err := db.Open(*coverBackfillDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer database.Close()
		if err := db.Migrate(database); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
		store := games.NewStore(database)
		igdbClient := igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
		svc := games.NewService(store, igdbClient, database)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		progress := func(done, total int) {
			if total == 0 {
				log.Printf("backfill: no pending rows")
				return
			}
			log.Printf("backfill: %d/%d (%.1f%%)", done, total, 100*float64(done)/float64(total))
		}
		done, err := svc.BackfillCovers(ctx, *coverBackfillBatch, progress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill stopped: %v (completed %d — safe to re-run)\n", err, done)
			os.Exit(1)
		}
		fmt.Printf("backfill: corrected covers for %d games\n", done)
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "backfill-editions" {
		editionBackfillCmd.Parse(os.Args[2:])
		cfg := config.Load()
		if cfg.IGDBClientID == "" {
			fmt.Fprintln(os.Stderr, "backfill-editions requires IGDB_CLIENT_ID (or TWITCH_OAUTH_ID)")
			os.Exit(1)
		}
		database, err := db.Open(*editionBackfillDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer database.Close()
		if err := db.Migrate(database); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
		store := games.NewStore(database)
		igdbClient := igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
		svc := games.NewService(store, igdbClient, database)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		progress := func(done, total int) {
			if total == 0 {
				log.Printf("backfill: no pending rows")
				return
			}
			log.Printf("backfill: %d/%d (%.1f%%)", done, total, 100*float64(done)/float64(total))
		}
		done, err := svc.BackfillEditions(ctx, *editionBackfillBatch, progress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill stopped: %v (completed %d — safe to re-run)\n", err, done)
			os.Exit(1)
		}
		fmt.Printf("backfill: fetched editions for %d games\n", done)
		return
	}

	cfg := config.Load()

	if err := os.MkdirAll(cfg.CoverDir, 0755); err != nil {
		log.Fatalf("create cover dir: %v", err)
	}
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}

		coverWorker := covers.NewWorker(database, cfg.CoverDir)
	coverWorker.Start()

	// One-time cleanup of cover URLs pointing at the defunct
	// images.cato.com host (legacy import). Cheap, idempotent SQL —
	// runs regardless of IGDB configuration.
	if n, err := games.PurgeDeadCoverSources(database); err != nil {
		log.Printf("startup: dead cover source purge failed: %v", err)
	} else if n > 0 {
		log.Printf("startup: cleared %d games pointing at dead cover host", n)
	}

	// One-time migration: ownership encoded as tags ("Switch", "PS5", …)
	// moves into library_items.platform, tag removed after the move.
	// Idempotent — no-ops once every mappable tag is gone.
	if n, err := games.MigratePlatformTags(database); err != nil {
		log.Printf("startup: platform tag migration failed: %v", err)
	} else if n > 0 {
		log.Printf("startup: migrated %d items from platform tags to ownership field", n)
	}

	srv := http.NewServer(cfg, database)
	log.Printf("cato listening on %s", cfg.ListenAddr)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Start()
	}()

	// Periodic maintenance: expired sessions and expired IGDB cache entries
	// are otherwise only removed lazily (or never). Run one sweep at startup,
	// then daily.
	maintenance := func() {
		if n, err := auth.CleanupExpiredSessions(database); err != nil {
			log.Printf("maintenance: session cleanup failed: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: deleted %d expired sessions", n)
		}
		if n, err := games.PurgeExpiredQueryCache(database); err != nil {
			log.Printf("maintenance: query cache purge failed: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: purged %d expired cache entries", n)
		}
	}
	maintenance()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			maintenance()
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil && err != nethttp.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	case s := <-sig:
		log.Printf("received %v, shutting down", s)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}
