package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cato/internal/config"
	"cato/internal/covers"
	"cato/internal/db"
	"cato/internal/http"
	"cato/internal/importer"
)

func main() {
	importCmd := flag.NewFlagSet("import-games", flag.ExitOnError)
	importInput := importCmd.String("input", "", "Postgres COPY dump SQL file")
	importDB := importCmd.String("db", "data/cato.db", "SQLite database path")

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
