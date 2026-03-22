package muni

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runExtractQuery executes query, writes results as TSV, returns (rows, sha256).
func runExtractQuery(ctx context.Context, pool *pgxpool.Pool, outDir, file, query string, columns []string) (int, string, error) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return 0, "", fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, "", fmt.Errorf("mkdir: %w", err)
	}

	path := filepath.Join(outDir, file)
	f, err := os.Create(path)
	if err != nil {
		return 0, "", fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	w := csv.NewWriter(io.MultiWriter(f, h))
	w.Comma = '\t'

	w.Write(columns)

	count := 0
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return 0, "", fmt.Errorf("scan row %d: %w", count+1, err)
		}
		record := make([]string, len(vals))
		for i, v := range vals {
			record[i] = formatValue(v)
		}
		w.Write(record)
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("rows: %w", err)
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return 0, "", fmt.Errorf("write: %w", err)
	}

	return count, hex.EncodeToString(h.Sum(nil)), nil
}

// formatValue converts a pgx value to its TSV string representation.
func formatValue(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case time.Time:
		return val.Format(time.RFC3339)
	case pgtype.Numeric:
		fv, err := val.Float64Value()
		if err != nil || !fv.Valid {
			return fmt.Sprintf("%v", val)
		}
		return strconv.FormatFloat(fv.Float64, 'f', 2, 64)
	default:
		return fmt.Sprintf("%v", val)
	}
}
