// Command fetcher is the operator interface for refreshing curated source
// data. It consolidates the four old fetch* binaries into one CLI with
// preview-then-confirm UX.
//
// Usage:
//
//	fetcher              interactive menu
//	fetcher gtfs         Thunder Bay GTFS static schedule
//	fetcher votes        eSCRIBE council meetings (all terms)
//	fetcher wards        Open North ward boundaries
//
// Every subcommand discovers URLs first, prints them, then prompts [y/N].
// No flags — this is a manual operator tool. For scheduled/CI runs, write
// a small wrapper that calls the underlying internal/{council,transit,wards}
// packages directly.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		runInteractive()
		return
	}

	switch os.Args[1] {
	case "gtfs":
		runGTFS()
	case "votes":
		runVotes()
	case "wards":
		runWards()
	case "chunks":
		runChunks()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `fetcher — refresh curated source data with preview + confirm

Usage:
  fetcher              interactive menu
  fetcher <cmd>

Subcommands:
  gtfs     Thunder Bay GTFS static schedule
  votes    eSCRIBE council meetings (all terms)
  wards    Open North ward boundaries
  chunks  Rebuild transit metric chunks for a date range

Every subcommand discovers its URLs first, prints them, then prompts [y/N].
Manual operator tool only — no flags. For scheduled or CI runs, write a
small wrapper that calls the internal/{council,transit,wards} packages
directly.
`)
}

// openPool connects to DATABASE_URL with a generous timeout. Used by every
// subcommand that needs DB writes (all except gtfs and wards).
func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	dbURL := config.Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable")
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// rootContext returns a long-running context (10 minutes) used by every
// subcommand. Long enough for the slowest fetch (votes scraper with PDFs).
func rootContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

// fail prints an error to stderr and exits 1. Used by all subcommands.
func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
