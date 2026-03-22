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
func writeBOD(outDir string, datasets []muni.Dataset) error {
	path := filepath.Join(outDir, "BOD.tsv")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Comma = '\t'
	w.Write([]string{"dataset", "plugin", "table", "source_url", "source_doc",
		"description", "collected", "license", "processor", "rows", "sha256"})
	for _, ds := range datasets {
		w.Write([]string{
			ds.File, ds.Plugin, ds.Table, ds.SourceURL, ds.SourceDoc,
			ds.Description, ds.Collected.Format(time.RFC3339),
			ds.License, ds.Processor, strconv.Itoa(ds.Rows), ds.SHA256,
		})
	}
	w.Flush()
	return w.Error()
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
