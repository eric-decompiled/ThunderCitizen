package muni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
)

// SchemaVersion is the BOD/bundle format version this server understands.
// Bump this when the BOD columns, plugin contract, or bundle layout change
// in a backwards-incompatible way.
//
// When publishing a bundle for a breaking change:
//  1. Bump SchemaVersion here
//  2. Deploy servers with the new version
//  3. Publish the new bundle (which declares the new schema_version in index.json)
//
// Old servers seeing a bundle with a higher schema_version will log and skip,
// not crash.
const SchemaVersion = 2

// Index is the pointer file that declares the current bundle to fetch.
// Served from e.g. data.thundercitizen.ca/index.json.
//
// The index itself is unsigned — security comes from the bundle's embedded
// SSH signature. A malicious index can only deny service (point at a bundle
// that fails to verify), not substitute data.
type Index struct {
	SchemaVersion     int    `json:"schema_version"`
	Bundle            string `json:"bundle"`
	MerkleRoot        string `json:"merkle_root"`
	SignerFingerprint string `json:"signer_fingerprint"`
	PublishedAt       string `json:"published_at"`
}

// FetchIndex downloads and parses the index JSON from the given URL.
func FetchIndex(ctx context.Context, indexURL string) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("muni: index request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("muni: index fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("muni: index HTTP %d from %s", resp.StatusCode, indexURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("muni: index read: %w", err)
	}

	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("muni: index parse: %w", err)
	}

	if idx.Bundle == "" {
		return nil, fmt.Errorf("muni: index missing bundle field")
	}
	return &idx, nil
}

// BundleURL resolves the bundle filename from index.json against the index
// URL's base, yielding an absolute URL for the zip.
func (i *Index) BundleURL(indexURL string) (string, error) {
	u, err := url.Parse(indexURL)
	if err != nil {
		return "", fmt.Errorf("muni: parse index URL: %w", err)
	}
	u.Path = path.Join(path.Dir(u.Path), i.Bundle)
	return u.String(), nil
}
