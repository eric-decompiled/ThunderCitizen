package muni

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PackRow is one row of muni_packs joined with the most-recent
// data_patch_log checksum per dataset in the pack (aggregated as count).
type PackRow struct {
	PackID       string
	UnitKind     UnitKind
	UnitStart    time.Time
	UnitEnd      time.Time
	DatasetCount int
	TotalRows    int64
	SignerFP     string
	BundleMerkle string
	AppliedAt    time.Time
	LastError    string
}

// ListPacks returns every row of muni_packs. Ordered so date-ranged
// packs come first (transit/budget/council), globals last. Within a
// kind, newest range first.
func ListPacks(ctx context.Context, pool *pgxpool.Pool) ([]PackRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT pack_id, unit_kind,
		       unit_start, unit_end,
		       dataset_count, total_rows,
		       signer_fp, bundle_merkle,
		       applied_at, COALESCE(last_error, '')
		FROM muni_packs
		ORDER BY
		  CASE unit_kind
		    WHEN 'global'       THEN 3
		    WHEN 'council_term' THEN 0
		    WHEN 'budget_year'  THEN 1
		    WHEN 'transit_day'  THEN 2
		    ELSE 4
		  END,
		  unit_start DESC NULLS LAST,
		  pack_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PackRow
	for rows.Next() {
		var p PackRow
		var kind string
		var start, end *time.Time
		if err := rows.Scan(&p.PackID, &kind, &start, &end,
			&p.DatasetCount, &p.TotalRows,
			&p.SignerFP, &p.BundleMerkle,
			&p.AppliedAt, &p.LastError); err != nil {
			return nil, err
		}
		p.UnitKind = UnitKind(kind)
		if start != nil {
			p.UnitStart = *start
		}
		if end != nil {
			p.UnitEnd = *end
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DevCacheBODSha returns the hash recorded in muni_dev_cache (empty if
// never populated).
func DevCacheBODSha(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var sha string
	err := pool.QueryRow(ctx,
		`SELECT bod_sha FROM muni_dev_cache WHERE id = 1`).Scan(&sha)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

// SetDevCacheBODSha records the hash we just applied so the next boot
// can skip the apply goroutine if BOD.tsv hasn't changed.
func SetDevCacheBODSha(ctx context.Context, pool *pgxpool.Pool, sha string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO muni_dev_cache (id, bod_sha, last_applied_at)
		 VALUES (1, $1, now())
		 ON CONFLICT (id) DO UPDATE SET
		    bod_sha = EXCLUDED.bod_sha,
		    last_applied_at = EXCLUDED.last_applied_at`, sha)
	return err
}
