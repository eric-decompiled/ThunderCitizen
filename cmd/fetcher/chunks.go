package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"thundercitizen/internal/transit"
)

// runChunks backfills the per-(route_id, date, band) metrics rollups in
// transit.route_band_chunk. Idempotent — re-running for the same date
// range overwrites existing rows. Intended to run nightly (via shell cron
// or by hand) after a service day closes; the metrics read path falls
// through to live builds for any date that hasn't been rolled up yet, so
// missing this step degrades performance but not correctness.
func runChunks() {
	ctx, cancel := rootContext()
	defer cancel()

	from, to, ok := promptChunkRange()
	if !ok {
		fmt.Println("cancelled")
		return
	}

	pool, err := openPool(ctx)
	if err != nil {
		fail("open pool: %v", err)
	}
	defer pool.Close()

	fmt.Printf("Building metric chunks for %s..%s\n", from.Format("2006-01-02"), to.Format("2006-01-02"))
	if !confirm() {
		fmt.Println("cancelled")
		return
	}

	total := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		n, err := transit.BuildChunksForDate(ctx, pool, d)
		if err != nil {
			fail("%s: %v", d.Format("2006-01-02"), err)
		}
		fmt.Printf("  %s: %d chunks\n", d.Format("2006-01-02"), n)
		total += n
	}
	fmt.Printf("Done — %d chunks across %d days.\n", total, daysBetween(from, to))
}

// promptChunkRange asks for a date range with a sensible default of
// "yesterday only". Accepts blank input on the second prompt to mean
// "single day". Returns Thunder Bay-local midnight times.
func promptChunkRange() (from, to time.Time, ok bool) {
	if !isatty(os.Stdin) {
		fmt.Fprintln(os.Stderr, "error: fetcher is a manual tool — run it from a terminal, not a pipe or cron")
		os.Exit(2)
	}
	reader := bufio.NewReader(os.Stdin)

	yesterday := transit.ServiceDate().AddDate(0, 0, -1)

	fmt.Printf("From date [%s]: ", yesterday.Format("2006-01-02"))
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		from = yesterday
	} else {
		t, err := time.ParseInLocation("2006-01-02", line, transit.TZ)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad date: %v\n", err)
			return time.Time{}, time.Time{}, false
		}
		from = t
	}

	fmt.Printf("To date [%s]: ", from.Format("2006-01-02"))
	line, _ = reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		to = from
	} else {
		t, err := time.ParseInLocation("2006-01-02", line, transit.TZ)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad date: %v\n", err)
			return time.Time{}, time.Time{}, false
		}
		to = t
	}

	if to.Before(from) {
		fmt.Fprintln(os.Stderr, "error: to date is before from date")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func daysBetween(from, to time.Time) int {
	return int(to.Sub(from).Hours()/24) + 1
}
