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

// UnitKind is the natural division of a data pack.
type UnitKind string

const (
	UnitBudgetYear  UnitKind = "budget_year"  // e.g. 2026
	UnitCouncilTerm UnitKind = "council_term" // e.g. 2022-2026
	UnitTransitDay  UnitKind = "transit_day"  // e.g. 2026-04-19
	UnitGlobal      UnitKind = "global"       // not date-bounded
)

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

	// Pack metadata — added in BOD schema v2. Older BODs default
	// PackID to "" and UnitKind to UnitGlobal so loaders don't break.
	PackID    string    // e.g. "budget-2026"
	UnitKind  UnitKind  // discriminator for UnitStart/UnitEnd
	UnitStart time.Time // inclusive; zero for global
	UnitEnd   time.Time // inclusive; zero for global
}

// HasPack reports whether the dataset declares pack metadata.
func (d Dataset) HasPack() bool { return d.PackID != "" }

// bodColumnsV1 is the original column set — kept so BODs published
// before the pack-metadata columns keep parsing.
var bodColumnsV1 = []string{
	"dataset", "plugin", "table", "source_url", "source_doc", "description",
	"collected", "license", "processor", "rows", "sha256",
}

// bodColumnsV2 appends pack_id, unit_kind, unit_start, unit_end.
var bodColumnsV2 = append(append([]string{}, bodColumnsV1...),
	"pack_id", "unit_kind", "unit_start", "unit_end")

// BODColumns returns the current (v2) column order. Callers writing a
// BOD.tsv use this; readers auto-detect v1 vs v2 from the header.
func BODColumns() []string {
	out := make([]string, len(bodColumnsV2))
	copy(out, bodColumnsV2)
	return out
}

// ParseBOD reads BOD.tsv from fsys and returns parsed dataset entries.
// It validates that each referenced file exists and its SHA256 matches.
// The header may be the v1 (11-col) or v2 (15-col) schema — v1 rows get
// default pack metadata (UnitKind=global, empty PackID/range).
func ParseBOD(fsys fs.FS) ([]Dataset, error) {
	f, err := fsys.Open(BODFile)
	if err != nil {
		return nil, fmt.Errorf("muni: open %s: %w", BODFile, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("muni: read BOD header: %w", err)
	}
	schema, err := detectSchema(header)
	if err != nil {
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
		if len(record) != schema.columnCount {
			return nil, fmt.Errorf("muni: BOD line %d: got %d columns, want %d", line, len(record), schema.columnCount)
		}

		ds, err := parseRow(record, schema, line)
		if err != nil {
			return nil, err
		}

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

type bodSchema struct {
	version     int
	columnCount int
}

func detectSchema(header []string) (bodSchema, error) {
	trim := make([]string, len(header))
	for i, c := range header {
		trim[i] = strings.TrimSpace(c)
	}
	if columnsEqual(trim, bodColumnsV2) {
		return bodSchema{version: 2, columnCount: len(bodColumnsV2)}, nil
	}
	if columnsEqual(trim, bodColumnsV1) {
		return bodSchema{version: 1, columnCount: len(bodColumnsV1)}, nil
	}
	return bodSchema{}, fmt.Errorf("muni: BOD header does not match v1 (%d cols) or v2 (%d cols): got %v",
		len(bodColumnsV1), len(bodColumnsV2), trim)
}

func columnsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func parseRow(record []string, schema bodSchema, line int) (Dataset, error) {
	collected, err := time.Parse(time.RFC3339, record[6])
	if err != nil {
		return Dataset{}, fmt.Errorf("muni: BOD line %d: bad collected timestamp: %w", line, err)
	}
	rows, err := strconv.Atoi(record[9])
	if err != nil {
		return Dataset{}, fmt.Errorf("muni: BOD line %d: bad rows: %w", line, err)
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
		UnitKind:    UnitGlobal,
	}

	if schema.version >= 2 {
		ds.PackID = record[11]
		if record[12] != "" {
			ds.UnitKind = UnitKind(record[12])
		}
		if record[13] != "" {
			t, err := time.Parse("2006-01-02", record[13])
			if err != nil {
				return Dataset{}, fmt.Errorf("muni: BOD line %d: bad unit_start: %w", line, err)
			}
			ds.UnitStart = t
		}
		if record[14] != "" {
			t, err := time.Parse("2006-01-02", record[14])
			if err != nil {
				return Dataset{}, fmt.Errorf("muni: BOD line %d: bad unit_end: %w", line, err)
			}
			ds.UnitEnd = t
		}
	}

	return ds, nil
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
