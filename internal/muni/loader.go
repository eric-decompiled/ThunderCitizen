package muni

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"thundercitizen/internal/munisign"
	"thundercitizen/internal/zipfs"
)

// Bundle is a downloaded and optionally verified muni data bundle.
type Bundle struct {
	FS           fs.FS
	Verification *munisign.Verification // nil if verification was skipped
	Index        *Index                 // nil if loaded directly from a zip URL
}

// Load fetches a muni bundle. The URL can be either:
//   - an index.json pointer (preferred) — server reads the index, resolves
//     the bundle name, downloads the zip, verifies
//   - a direct .zip URL (legacy) — downloads and verifies directly
//
// If pubKey is nil, signature verification is skipped.
func Load(ctx context.Context, u string, pubKey []byte) (*Bundle, error) {
	if strings.HasSuffix(u, ".zip") {
		return loadZip(ctx, u, pubKey)
	}
	return loadIndex(ctx, u, pubKey)
}

func loadIndex(ctx context.Context, indexURL string, pubKey []byte) (*Bundle, error) {
	idx, err := FetchIndex(ctx, indexURL)
	if err != nil {
		return nil, err
	}

	log.Info("index",
		"schema_version", idx.SchemaVersion,
		"bundle", idx.Bundle,
		"merkle", truncate(idx.MerkleRoot, 12),
		"signer", idx.SignerFingerprint,
		"published_at", idx.PublishedAt)

	if idx.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("muni: index schema_version=%d is newer than supported=%d — upgrade the server", idx.SchemaVersion, SchemaVersion)
	}
	if idx.SchemaVersion < SchemaVersion {
		log.Warn("index schema is older than server — skipping upgrade",
			"index", idx.SchemaVersion, "server", SchemaVersion)
	}

	bundleURL, err := idx.BundleURL(indexURL)
	if err != nil {
		return nil, err
	}

	b, err := loadZip(ctx, bundleURL, pubKey)
	if err != nil {
		return nil, err
	}
	b.Index = idx

	// Cross-check: the index's merkle_root should match what we compute.
	if idx.MerkleRoot != "" && b.Verification != nil && idx.MerkleRoot != b.Verification.MerkleRoot {
		return nil, fmt.Errorf("muni: merkle mismatch — index=%s bundle=%s",
			idx.MerkleRoot[:12], b.Verification.MerkleRoot[:12])
	}

	return b, nil
}

func loadZip(ctx context.Context, zipURL string, pubKey []byte) (*Bundle, error) {
	fsys, err := zipfs.Fetch(ctx, zipURL)
	if err != nil {
		return nil, fmt.Errorf("muni: download: %w", err)
	}

	var v *munisign.Verification
	if pubKey != nil {
		v, err = munisign.VerifyFS(fsys, pubKey)
		if err != nil {
			return nil, fmt.Errorf("muni: verify: %w", err)
		}
	}

	return &Bundle{FS: fsys, Verification: v}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
