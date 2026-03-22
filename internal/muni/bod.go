// Package muni handles loading, verifying, and applying signed municipal
// data bundles. A bundle is a flat directory (or zip) of TSV data files,
// a BOD.tsv provenance manifest, and an SSH signature (manifest.sig).
package muni

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// BODFile is the conventional filename for the Bill of Data inside a bundle.
const BODFile = "BOD.tsv"

// Dataset describes one data file entry in the Bill of Data.
type Dataset struct {
	File        string    // filename in the bundle, e.g. "councillors.tsv"
	Plugin      string    // name of the plugin that handles this dataset
	Table       string    // target Postgres table
	SourceURL   string    // canonical URL of the original data source
	SourceDoc   string    // direct URL or relative path to .sources.tsv
	Description string    // one-line human description
	Collected   time.Time // when data was collected
	License     string    // e.g. "public-record", "OGL-ON"
	Processor   string    // e.g. "hand-curated", "scraped+llm", "pg_dump"
	Rows        int       // expected row count (excluding header)
	SHA256      string    // hex hash of the file contents
}

// BOD column order. This is the canonical header for BOD.tsv.
var bodColumns = []string{
	"dataset", "plugin", "table", "source_url", "source_doc", "description",
	"collected", "license", "processor", "rows", "sha256",
}

// ParseBOD reads BOD.tsv from fsys and returns parsed dataset entries.
// It validates that each referenced file exists and its SHA256 matches.
func ParseBOD(fsys fs.FS) ([]Dataset, error) {
	f, err := fsys.Open(BODFile)
	if err != nil {
		return nil, fmt.Errorf("muni: open %s: %w", BODFile, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true

	// Read and validate header.
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("muni: read BOD header: %w", err)
	}
	if err := validateHeader(header); err != nil {
		return nil, err
	}

	var datasets []Dataset
	for line := 2; ; line++ {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("muni: BOD line %d: %w", line, err)
		}
		if len(record) != len(bodColumns) {
			return nil, fmt.Errorf("muni: BOD line %d: got %d columns, want %d", line, len(record), len(bodColumns))
		}

		collected, err := time.Parse(time.RFC3339, record[6])
		if err != nil {
			return nil, fmt.Errorf("muni: BOD line %d: bad collected timestamp: %w", line, err)
		}
		rows, err := strconv.Atoi(record[9])
		if err != nil {
			return nil, fmt.Errorf("muni: BOD line %d: bad rows: %w", line, err)
		}

		ds := Dataset{
			File:        record[0],
			Plugin:      record[1],
			Table:       record[2],
			SourceURL:   record[3],
			SourceDoc:   record[4],
			Description: record[5],
			Collected:   collected,
			License:     record[7],
			Processor:   record[8],
			Rows:        rows,
			SHA256:      record[10],
		}

		// Validate file exists and hash matches.
		if err := validateFile(fsys, ds); err != nil {
			return nil, fmt.Errorf("muni: BOD line %d (%s): %w", line, ds.File, err)
		}

		datasets = append(datasets, ds)
	}

	if len(datasets) == 0 {
		return nil, fmt.Errorf("muni: BOD.tsv has no dataset entries")
	}
	return datasets, nil
}

// IsProvenance returns true if the filename is a .sources.tsv provenance
// file. These are hashed and signed but not loaded into the database.
func IsProvenance(filename string) bool {
	return strings.HasSuffix(filename, ".sources.tsv")
}

// IsDirectURL returns true if the source_doc value is an absolute URL
// (as opposed to a relative path to a file in the bundle).
func IsDirectURL(sourceDoc string) bool {
	return strings.HasPrefix(sourceDoc, "http://") || strings.HasPrefix(sourceDoc, "https://")
}

func validateHeader(header []string) error {
	if len(header) != len(bodColumns) {
		return fmt.Errorf("muni: BOD header has %d columns, want %d", len(header), len(bodColumns))
	}
	for i, want := range bodColumns {
		got := strings.TrimSpace(header[i])
		if got != want {
			return fmt.Errorf("muni: BOD column %d: got %q, want %q", i, got, want)
		}
	}
	return nil
}

func validateFile(fsys fs.FS, ds Dataset) error {
	f, err := fsys.Open(ds.File)
	if err != nil {
		return fmt.Errorf("file not found: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != ds.SHA256 {
		return fmt.Errorf("sha256 mismatch: BOD says %s, file is %s", ds.SHA256, actual)
	}
	return nil
}
