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
// If pubKey is nil, signature verification is skipped. Prefer
// LoadWithTrust in production code — it enforces the embedded
// approved+revoked trust store rather than a single caller-supplied key.
func Load(ctx context.Context, u string, pubKey []byte) (*Bundle, error) {
	v := &singleKeyVerifier{pubKey: pubKey}
	if strings.HasSuffix(u, ".zip") {
		return loadZip(ctx, u, v)
	}
	return loadIndex(ctx, u, v)
}

// LoadWithTrust is the production entry point: verification uses the
// embedded approved/revoked trust store. A bundle signed by a revoked
// key fails loudly; a bundle signed by an unknown key fails too. There
// is no escape hatch at runtime — rotating trust requires a new binary.
func LoadWithTrust(ctx context.Context, u string, trust *munisign.Trust) (*Bundle, error) {
	v := &trustVerifier{trust: trust}
	if strings.HasSuffix(u, ".zip") {
		return loadZip(ctx, u, v)
	}
	return loadIndex(ctx, u, v)
}

// verifier lets loader choose between legacy single-key and trust-store
// verification without duplicating the download plumbing.
type verifier interface {
	verify(fsys fs.FS) (*munisign.Verification, error)
	// skip reports whether verification is a no-op (legacy dev mode
	// that passed pubKey=nil). The new trust-store path never skips.
	skip() bool
}

type singleKeyVerifier struct{ pubKey []byte }

func (s *singleKeyVerifier) skip() bool { return s.pubKey == nil }
func (s *singleKeyVerifier) verify(fsys fs.FS) (*munisign.Verification, error) {
	return munisign.VerifyFS(fsys, s.pubKey)
}

type trustVerifier struct{ trust *munisign.Trust }

func (t *trustVerifier) skip() bool { return t.trust == nil }
func (t *trustVerifier) verify(fsys fs.FS) (*munisign.Verification, error) {
	return munisign.VerifyFSWithTrust(fsys, t.trust)
}

func loadIndex(ctx context.Context, indexURL string, v verifier) (*Bundle, error) {
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

	b, err := loadZip(ctx, bundleURL, v)
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

func loadZip(ctx context.Context, zipURL string, v verifier) (*Bundle, error) {
	fsys, err := zipfs.Fetch(ctx, zipURL)
	if err != nil {
		return nil, fmt.Errorf("muni: download: %w", err)
	}

	var ver *munisign.Verification
	if !v.skip() {
		ver, err = v.verify(fsys)
		if err != nil {
			return nil, fmt.Errorf("muni: verify: %w", err)
		}
	}

	return &Bundle{FS: fsys, Verification: ver}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
