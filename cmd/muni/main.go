// Command muni extracts municipal data from the dev database and publishes
// signed TSV bundles to data.thundercitizen.ca.
//
// Usage:
//
//	go run ./cmd/muni extract              # dev DB → data/muni/*.tsv + BOD.tsv
//	go run ./cmd/muni extract -out <dir>   # custom output dir
//	go run ./cmd/muni publish              # zip + upload data/muni → DO Spaces
//	go run ./cmd/muni publish -dry-run     # build but don't upload
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/config"
	"thundercitizen/internal/muni"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "extract":
		runExtract(os.Args[2:])
	case "publish":
		runPublish(os.Args[2:])
	default:
		usage()
	}
}

func runExtract(args []string) {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	out := fs.String("out", "data/muni", "output directory")
	if err := fs.Parse(args); err != nil {
		fail("flags: %v", err)
	}

	dbURL := config.Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fail("connect: %v", err)
	}
	defer pool.Close()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail("mkdir: %v", err)
	}

	var allDatasets []muni.Dataset
	for _, plugin := range muni.Registry() {
		logf("extract  plugin=%s", plugin.Name())
		datasets, err := plugin.Extract(ctx, pool, *out)
		if err != nil {
			fail("plugin %s: %v", plugin.Name(), err)
		}
		for _, ds := range datasets {
			logf("  %s  rows=%d  sha256=%s\n", ds.File, ds.Rows, ds.SHA256[:12])
		}
		allDatasets = append(allDatasets, datasets...)
	}

	if err := writeBOD(*out, allDatasets); err != nil {
		fail("BOD: %v", err)
	}
	logf("wrote BOD.tsv (%d datasets)\n", len(allDatasets))
	logf("output: %s/\n", *out)
}

// writeBOD generates BOD.tsv from the collected dataset entries.
// Emits the v2 (15-column) schema — pack_id, unit_kind, unit_start,
// unit_end appended so admin tooling and the server can group
// datasets into logical packs. Datasets without a declared pack
// render as unit_kind=global with empty range cells.
func writeBOD(outDir string, datasets []muni.Dataset) error {
	path := filepath.Join(outDir, "BOD.tsv")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Comma = '\t'
	w.Write(muni.BODColumns())
	for _, ds := range datasets {
		kind := string(ds.UnitKind)
		if kind == "" {
			kind = string(muni.UnitGlobal)
		}
		w.Write([]string{
			ds.File, ds.Plugin, ds.Table, ds.SourceURL, ds.SourceDoc,
			ds.Description, ds.Collected.Format(time.RFC3339),
			ds.License, ds.Processor, strconv.Itoa(ds.Rows), ds.SHA256,
			ds.PackID, kind,
			formatDate(ds.UnitStart), formatDate(ds.UnitEnd),
		})
	}
	w.Flush()
	return w.Error()
}

// formatDate renders a pack range date in ISO form. Zero values (global
// packs) render as the empty string so BOD.tsv stays diff-clean.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
	if len(format) == 0 || format[len(format)-1] != '\n' {
		fmt.Fprintln(os.Stderr)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: muni <command> [args]

commands:
  extract [-out DIR]          dev DB → TSV files + BOD.tsv (default: data/muni/)
  publish [-dir DIR] [-dry-run]
                              zip + upload bundle to DO Spaces (requires
                              manifest.sig; run munisign sign first)`)
	os.Exit(2)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}
