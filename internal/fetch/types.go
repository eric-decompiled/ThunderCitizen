// Package fetch defines the shared types used by the per-source data fetchers
// and the cmd/fetcher CLI that drives them.
//
// Each upstream data source (budget, gtfs, votes, wards) lives in its own
// internal package and exposes:
//
//	DiscoverSources(ctx, opts) ([]fetch.Source, error)   // cheap; safe to call without confirmation
//	Fetch(ctx, opts, ...) error                          // does the heavy downloads
//
// The CLI runs Discover first, prints the URL list, awaits a y/N prompt,
// and only then calls Fetch.
package fetch

// Source describes one URL that the operator-facing CLI will print
// before downloading anything.
type Source struct {
	Label string // short human label, e.g. "FIR 2024 Schedule 10"
	URL   string
}
