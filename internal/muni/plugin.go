package muni

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plugin is the contract for a data type that knows how to extract itself
// from a live database and load itself back into one. Each plugin owns
// the SQL strategy for its dataset family — including FK resolution via
// natural keys when needed.
type Plugin interface {
	// Name is the stable identifier used in BOD.tsv's `plugin` column.
	Name() string

	// Extract queries the dev database and writes one or more TSV files
	// into outDir. Returns one Dataset per file (BOD entries).
	Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error)

	// Apply loads one TSV file into the database. The dataset's filename,
	// table, and sha256 are already validated; the plugin chooses the
	// INSERT strategy (ON CONFLICT, JOIN-based, etc).
	Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (rowsLoaded int, err error)
}

var registered = []Plugin{}

// Register adds a plugin to the global registry. Order matters for
// extract and apply — register dependencies first.
func Register(p Plugin) {
	registered = append(registered, p)
}

// Registry returns all registered plugins in registration order.
func Registry() []Plugin {
	return registered
}

// Lookup finds a plugin by name.
func Lookup(name string) Plugin {
	for _, p := range registered {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// ─── Shared helpers for plugin implementations ──────────────────────────

// LoadStaging reads a TSV from fsys, COPYs it into a temp staging table
// with all-text columns, and returns the header. The caller does the
// final INSERT from staging into the target table.
func LoadStaging(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset, stagingTable string) ([]string, error) {
	f, err := fsys.Open(ds.File)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	var rows [][]any
	for line := 2; ; line++ {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(record) != len(header) {
			return nil, fmt.Errorf("line %d: got %d columns, want %d", line, len(record), len(header))
		}
		row := make([]any, len(record))
		for i, v := range record {
			row[i] = v
		}
		rows = append(rows, row)
	}

	if len(rows) != ds.Rows {
		return nil, fmt.Errorf("row count mismatch: BOD says %d, file has %d", ds.Rows, len(rows))
	}

	colDefs := make([]string, len(header))
	for i, col := range header {
		colDefs[i] = fmt.Sprintf(`"%s" text`, col)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TEMP TABLE %s (%s) ON COMMIT DROP`,
		stagingTable, strings.Join(colDefs, ", "))); err != nil {
		return nil, fmt.Errorf("create staging table: %w", err)
	}

	if _, err := tx.CopyFrom(ctx, pgx.Identifier{stagingTable}, header, pgx.CopyFromRows(rows)); err != nil {
		return nil, fmt.Errorf("COPY INTO %s: %w", stagingTable, err)
	}

	return header, nil
}

// CastExpressions builds NULLIF/cast expressions for each TSV column,
// based on the target table's actual column types. Use these in an
// INSERT...SELECT to convert text columns from staging into typed values.
func CastExpressions(ctx context.Context, tx pgx.Tx, table string, header []string) ([]string, error) {
	info, err := getColumnInfo(ctx, tx, table, header)
	if err != nil {
		return nil, err
	}
	exprs := make([]string, len(header))
	for i, col := range header {
		ci := info[col]
		if ci.nullable {
			exprs[i] = fmt.Sprintf(`NULLIF(s."%s", '')::%s`, col, ci.castType)
		} else {
			exprs[i] = fmt.Sprintf(`s."%s"::%s`, col, ci.castType)
		}
	}
	return exprs, nil
}

// UpsertFromStaging generates INSERT INTO table FROM staging with
// ON CONFLICT (conflictKeys) DO UPDATE — newer bundle values always win
// for non-key columns. If conflictKeys is empty, falls back to
// ON CONFLICT DO NOTHING (e.g. when the caller has already resolved
// deduplication via a custom JOIN-based insert).
func UpsertFromStaging(ctx context.Context, tx pgx.Tx, table, stagingTable string, header, conflictKeys []string) error {
	casts, err := CastExpressions(ctx, tx, table, header)
	if err != nil {
		return err
	}
	colList := quotedCols(header)

	conflict := "ON CONFLICT DO NOTHING"
	if len(conflictKeys) > 0 {
		keySet := make(map[string]bool, len(conflictKeys))
		for _, k := range conflictKeys {
			keySet[k] = true
		}
		var setClauses []string
		for _, c := range header {
			if keySet[c] {
				continue
			}
			setClauses = append(setClauses, fmt.Sprintf(`"%s" = EXCLUDED."%s"`, c, c))
		}
		if len(setClauses) == 0 {
			conflict = fmt.Sprintf("ON CONFLICT (%s) DO NOTHING", quotedCols(conflictKeys))
		} else {
			conflict = fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s",
				quotedCols(conflictKeys), strings.Join(setClauses, ", "))
		}
	}

	_, err = tx.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (%s) SELECT %s FROM %s s %s`,
		table, colList, strings.Join(casts, ", "), stagingTable, conflict))
	if err != nil {
		return fmt.Errorf("INSERT INTO %s: %w", table, err)
	}
	return nil
}

func quotedCols(header []string) string {
	parts := make([]string, len(header))
	for i, c := range header {
		parts[i] = fmt.Sprintf(`"%s"`, c)
	}
	return strings.Join(parts, ", ")
}

// ExtractToTSV is a helper that runs a query and writes the results to
// outDir/file as a TSV. Returns row count and SHA-256 hash.
func ExtractToTSV(ctx context.Context, pool *pgxpool.Pool, outDir, file, query string, columns []string) (int, string, error) {
	return runExtractQuery(ctx, pool, outDir, file, query, columns)
}
