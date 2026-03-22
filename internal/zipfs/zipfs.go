// Package zipfs downloads a zip archive from a URL and returns its contents
// as an fs.FS. The zip is held in memory — suitable for small data bundles
// (patch SQL files, config packs) but not multi-GB archives.
package zipfs

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"

	"thundercitizen/internal/logger"
)

var log = logger.New("zipfs")

// Fetch downloads the zip at url and returns an fs.FS backed by the
// in-memory zip contents. The returned FS is read-only and valid for
// the lifetime of the caller (the underlying byte slice stays live).
func Fetch(ctx context.Context, url string) (fs.FS, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("zipfs: building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zipfs: download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zipfs: HTTP %d from %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("zipfs: reading response: %w", err)
	}

	log.Info("downloaded zip", "url", url, "bytes", len(data))

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zipfs: opening zip: %w", err)
	}

	return reader, nil
}
