package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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

	srv := http.NewServer(cfg, database)
	log.Printf("cato listening on %s", cfg.ListenAddr)

	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")
}
