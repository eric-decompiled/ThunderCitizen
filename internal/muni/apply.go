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
// signed the bundle.
//
// Returns the count of newly loaded datasets. Skips datasets whose sha256
// already appears in data_patch_log. Errors on checksum drift.
func Apply(ctx context.Context, pool *pgxpool.Pool, b *Bundle) (int, error) {
	datasets, err := ParseBOD(b.FS)
	if err != nil {
		return 0, err
	}

	signer := ""
	if b.Verification != nil {
		signer = b.Verification.SignerFingerprint
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
	for _, ds := range datasets {
		n, err := applyDataset(ctx, pool, b.FS, ds, signer)
		if err != nil {
			return applied, fmt.Errorf("muni: applying %s: %w", ds.File, err)
		}
		applied += n
	}
	return applied, nil
}

// applyDataset dispatches to the dataset's plugin in a single transaction.
// Returns 1 if newly applied, 0 if skipped (already applied with same hash).
func applyDataset(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, ds Dataset, signer string) (int, error) {
	// Check if already applied.
	latest, err := latestAction(ctx, pool, ds.File)
	if err != nil {
		return 0, err
	}
	if latest != nil && latest.action == "apply" {
		if latest.checksum == ds.SHA256 {
			log.Info("skipped (already applied)", "dataset", ds.File)
			return 0, nil
		}
		return 0, fmt.Errorf("checksum drift: applied %s, BOD says %s — publish a new bundle", latest.checksum[:12], ds.SHA256[:12])
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
