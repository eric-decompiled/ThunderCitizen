package muni

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/logger"
)

var log = logger.New("muni")

// Apply reads BOD.tsv from the bundle, dispatches each dataset to its
// plugin, and tracks applied datasets in data_patch_log including who
// signed the bundle. On a verified signature, any dataset whose hash
// drifted from the last-applied version is re-applied (the signed BOD
// is authoritative); on an unsigned dev bundle, drift is re-applied
// with a warning so local iteration still works.
//
// Returns the count of newly loaded (or re-loaded) datasets.
func Apply(ctx context.Context, pool *pgxpool.Pool, b *Bundle) (int, error) {
	datasets, err := ParseBOD(b.FS)
	if err != nil {
		return 0, err
	}

	signer := ""
	merkle := ""
	verified := false
	if b.Verification != nil {
		signer = b.Verification.SignerFingerprint
		merkle = b.Verification.MerkleRoot
		verified = true
		log.Info("verified bundle",
			"merkle", b.Verification.MerkleRoot[:12],
			"signer", b.Verification.SignerKey,
			"fingerprint", b.Verification.SignerFingerprint)
	} else {
		log.Warn("unverified bundle (no signing key configured)")
	}

	log.Info("BOD loaded", "datasets", len(datasets))
	for _, ds := range datasets {
		log.Info("dataset", "file", ds.File, "plugin", ds.Plugin, "table", ds.Table, "rows", ds.Rows, "sha256", ds.SHA256[:12])
	}

	applied := 0
	packErrors := make(map[string]string)
	for _, ds := range datasets {
		n, err := applyDataset(ctx, pool, b.FS, ds, signer, verified)
		if err != nil {
			// Record the failure against the pack (if any) so the
			// admin page can surface it, but keep going — a single
			// bad dataset shouldn't block unrelated packs.
			if ds.PackID != "" {
				packErrors[ds.PackID] = err.Error()
			}
			log.Error("apply failed", "dataset", ds.File, "err", err)
			continue
		}
		applied += n
	}

	if err := upsertPacks(ctx, pool, datasets, signer, merkle, packErrors); err != nil {
		log.Warn("pack registry update failed", "err", err)
	}

	return applied, nil
}

// applyDataset dispatches to the dataset's plugin in a single transaction.
// Returns 1 if newly applied or re-applied on drift, 0 if skipped.
func applyDataset(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, ds Dataset, signer string, verified bool) (int, error) {
	latest, err := latestAction(ctx, pool, ds.File)
	if err != nil {
		return 0, err
	}
	if latest != nil && latest.action == "apply" {
		if latest.checksum == ds.SHA256 {
			log.Info("skipped (already applied)", "dataset", ds.File)
			return 0, nil
		}
		// Checksum drift. The bundle's signature already verified at
		// the loader boundary, so the new hash is authoritative — re-
		// apply and record the transition. For unsigned dev bundles
		// drift still re-applies but with a WARN so the operator
		// notices local edits.
		if verified {
			log.Info("checksum updated, re-applying",
				"dataset", ds.File,
				"old", latest.checksum[:12],
				"new", ds.SHA256[:12],
				"signer", signer)
		} else {
			log.Warn("checksum drift on unsigned bundle, re-applying",
				"dataset", ds.File,
				"old", latest.checksum[:12],
				"new", ds.SHA256[:12])
		}
	}

	plugin := Lookup(ds.Plugin)
	if plugin == nil {
		return 0, fmt.Errorf("unknown plugin %q for %s", ds.Plugin, ds.File)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rowsLoaded, err := plugin.Apply(ctx, tx, fsys, ds)
	if err != nil {
		return 0, err
	}
	log.Info("loaded", "dataset", ds.File, "plugin", ds.Plugin, "table", ds.Table, "rows", rowsLoaded)

	if _, err := tx.Exec(ctx,
		`INSERT INTO data_patch_log (patch_id, action, checksum, signer) VALUES ($1, 'apply', $2, $3)`,
		ds.File, ds.SHA256, signer); err != nil {
		return 0, fmt.Errorf("recording apply: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return 1, nil
}

// upsertPacks groups the BOD datasets by pack_id and upserts one row
// per pack into muni_packs. Packs with empty pack_id (v1-schema BODs
// or datasets that explicitly opt out) are skipped.
func upsertPacks(ctx context.Context, pool *pgxpool.Pool, datasets []Dataset, signer, merkle string, packErrors map[string]string) error {
	type packAgg struct {
		kind      UnitKind
		start     time.Time
		end       time.Time
		count     int
		totalRows int64
	}
	packs := make(map[string]*packAgg)
	for _, ds := range datasets {
		if ds.PackID == "" {
			continue
		}
		p, ok := packs[ds.PackID]
		if !ok {
			p = &packAgg{kind: ds.UnitKind, start: ds.UnitStart, end: ds.UnitEnd}
			packs[ds.PackID] = p
		}
		p.count++
		p.totalRows += int64(ds.Rows)
		// Widen the range if datasets in the pack disagree — should
		// not happen for well-formed BODs but tolerate it.
		if !ds.UnitStart.IsZero() && (p.start.IsZero() || ds.UnitStart.Before(p.start)) {
			p.start = ds.UnitStart
		}
		if !ds.UnitEnd.IsZero() && (p.end.IsZero() || ds.UnitEnd.After(p.end)) {
			p.end = ds.UnitEnd
		}
	}

	for id, p := range packs {
		var startArg, endArg any
		if !p.start.IsZero() {
			startArg = p.start
		}
		if !p.end.IsZero() {
			endArg = p.end
		}
		var errArg any
		if msg, ok := packErrors[id]; ok {
			errArg = msg
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO muni_packs
				(pack_id, unit_kind, unit_start, unit_end,
				 dataset_count, total_rows, signer_fp, bundle_merkle,
				 applied_at, last_error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), $9)
			ON CONFLICT (pack_id) DO UPDATE SET
				unit_kind     = EXCLUDED.unit_kind,
				unit_start    = EXCLUDED.unit_start,
				unit_end      = EXCLUDED.unit_end,
				dataset_count = EXCLUDED.dataset_count,
				total_rows    = EXCLUDED.total_rows,
				signer_fp     = EXCLUDED.signer_fp,
				bundle_merkle = EXCLUDED.bundle_merkle,
				applied_at    = EXCLUDED.applied_at,
				last_error    = EXCLUDED.last_error`,
			id, string(p.kind), startArg, endArg,
			p.count, p.totalRows, signer, merkle, errArg)
		if err != nil {
			return fmt.Errorf("upsert pack %s: %w", id, err)
		}
	}
	return nil
}

type logEntry struct {
	action   string
	checksum string
	at       time.Time
}

type colInfo struct {
	castType string
	nullable bool
}

// getColumnInfo returns type and nullability for each column in the target table.
func getColumnInfo(ctx context.Context, tx pgx.Tx, table string, columns []string) (map[string]colInfo, error) {
	rows, err := tx.Query(ctx,
		`SELECT column_name, data_type, is_nullable
		 FROM information_schema.columns
		 WHERE table_name = $1`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	info := make(map[string]colInfo)
	for rows.Next() {
		var name, typ, nullable string
		if err := rows.Scan(&name, &typ, &nullable); err != nil {
			return nil, err
		}
		ci := colInfo{nullable: nullable == "YES"}
		switch typ {
		case "integer":
			ci.castType = "integer"
		case "bigint":
			ci.castType = "bigint"
		case "numeric":
			ci.castType = "numeric"
		case "boolean":
			ci.castType = "boolean"
		case "date":
			ci.castType = "date"
		case "timestamp with time zone":
			ci.castType = "timestamptz"
		case "timestamp without time zone":
			ci.castType = "timestamp"
		default:
			ci.castType = "text"
		}
		info[name] = ci
	}

	result := make(map[string]colInfo, len(columns))
	for _, col := range columns {
		ci, ok := info[col]
		if !ok {
			return nil, fmt.Errorf("column %q not found in table %s", col, table)
		}
		result[col] = ci
	}
	return result, nil
}

func latestAction(ctx context.Context, pool *pgxpool.Pool, patchID string) (*logEntry, error) {
	row := pool.QueryRow(ctx,
		`SELECT action, checksum, at FROM data_patch_log
		 WHERE patch_id = $1
		 ORDER BY at DESC, id DESC
		 LIMIT 1`, patchID)
	var e logEntry
	if err := row.Scan(&e.action, &e.checksum, &e.at); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}
