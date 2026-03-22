// Command seedtransit fills the dev database with realistic-looking
// transit metric chunks so the chunk-driven UI (/transit/metrics,
// /transit/routes) has something to display when working in a fresh
// dev environment.
//
// ┌──────────────────────────────────────────────────────────────────┐
// │  DEV ONLY — NOT BUNDLED INTO THE PRODUCTION DOCKER IMAGE.        │
// │                                                                  │
// │  The Dockerfile enumerates the binaries it builds explicitly     │
// │  (server, fetcher, summarize, etc.) and seedtransit is NOT on    │
// │  that list. Do not add it. To use this tool, build from source:  │
// │                                                                  │
// │      go run ./cmd/seedtransit                                    │
// │                                                                  │
// │  or build a binary into bin/ which is gitignored:                │
// │                                                                  │
// │      go build -o bin/seedtransit ./cmd/seedtransit               │
// │      ./bin/seedtransit                                           │
// └──────────────────────────────────────────────────────────────────┘
//
// Defaults: 30 days ending today, hardcoded seed (42) for reproducibility,
// linear improvement from "bad" service to "good" service over the range.
// Same seed + same date range = byte-identical output.
//
// Usage:
//
//	seedtransit                       # 30 days, seed=42, ending today
//	seedtransit -days 14              # different range
//	seedtransit -seed 123             # different randomness
//	seedtransit -end 2026-04-10       # range ending on a specific date
//	seedtransit -clean                # delete previously-seeded rows first
//
// What it writes:
//
//   - transit.route_band_chunk — one row per (route, date, band).
//     UPSERTed by primary key, so re-running is idempotent.
//   - transit.cancellation     — synthetic per-trip rows backing the
//     cancel log on the metrics tab. trip_id always starts with "seed_"
//     so -clean can find them without touching real data.
//   - transit.route            — minimal placeholder rows ONLY when the
//     table is empty (fresh DB without GTFS loaded). If GTFS is already
//     loaded, the existing routes are reused untouched.
//
// What it does NOT write: stop_delay, stop_visit, route_baseline,
// route_pattern_stop, scheduled_stop. The chunk-first read path doesn't
// need any of those — chunks are self-contained — and skipping them
// keeps the seeder fast and self-sufficient.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/config"
)

func main() {
	var (
		days  = flag.Int("days", 30, "number of days of chunks to seed (ending on -end)")
		seed  = flag.Int64("seed", 42, "PRNG seed for reproducible synthesis")
		end   = flag.String("end", "", "last date to seed (YYYY-MM-DD); default = today")
		clean = flag.Bool("clean", false, "delete previously-seeded chunks in the range before re-seeding")
	)
	flag.Parse()

	endDate := time.Now().UTC()
	if *end != "" {
		parsed, err := time.Parse("2006-01-02", *end)
		if err != nil {
			fail("parse -end: %v", err)
		}
		endDate = parsed
	}
	endDate = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, time.UTC)

	if *days < 1 {
		fail("-days must be >= 1, got %d", *days)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dbURL := config.Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable")

	// Run migrations first — this is a dev tool, but if the dev DB is
	// stale (e.g. the server hasn't been bounced since a new migration
	// landed) we want the seeder to bring it up rather than fail with
	// "relation does not exist".
	if err := runMigrations(dbURL); err != nil {
		fail("migrate: %v", err)
	}

	pool, err := openPool(ctx, dbURL)
	if err != nil {
		fail("connect: %v", err)
	}
	defer pool.Close()

	fromDate := endDate.AddDate(0, 0, -(*days - 1))
	fmt.Printf("seedtransit: %s → %s (%d days, seed=%d)\n",
		fromDate.Format("2006-01-02"), endDate.Format("2006-01-02"), *days, *seed)

	s := NewSeeder(pool, *seed)

	if *clean {
		n, err := s.Clean(ctx, fromDate, endDate)
		if err != nil {
			fail("clean: %v", err)
		}
		fmt.Printf("  cleaned %d existing chunk rows\n", n)
	}

	summary, err := s.Run(ctx, fromDate, endDate)
	if err != nil {
		fail("seed: %v", err)
	}
	summarize(summary, *days)
}

// summarize prints what got written: total chunks, the number of routes
// they cover, and the cancellation count. One chunk per (route, date, band),
// so the total is routes × days × 3 bands.
func summarize(s Summary, days int) {
	fmt.Printf("  wrote %d chunks across %d days for %d routes\n", s.Chunks, days, s.Routes)
	fmt.Printf("  cancellations: %d trips\n", s.Cancellations)
	fmt.Println("done.")
}

// openPool connects to the given URL with a short ping. Same pattern as
// cmd/fetcher.
func openPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
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

// runMigrations applies any pending migrations from ./migrations using
// golang-migrate. Same call shape as cmd/server's startup runner so the
// seeder and the server agree on schema state.
func runMigrations(dbURL string) error {
	m, err := migrate.New("file://migrations", dbURL)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
