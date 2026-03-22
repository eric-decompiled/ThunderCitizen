package transit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"thundercitizen/internal/fetch"
)

// DiscoverGTFSSources returns the single canonical GTFS static download URL.
// No network call required — the URL is constant.
func DiscoverGTFSSources(_ context.Context) ([]fetch.Source, error) {
	return []fetch.Source{
		{
			Label: "Thunder Bay GTFS static schedule",
			URL:   gtfsURL,
		},
	}, nil
}

// FetchGTFS downloads the GTFS zip from gtfsURL and extracts it into
// static/transit/gtfs/. The running server's GTFSRefresher will pick up
// the new files on its next 4-hour reload tick (or immediately if the
// operator restarts the server).
func FetchGTFS(ctx context.Context) error {
	fmt.Printf("Downloading %s...\n", gtfsURL)
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gtfsURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading GTFS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, gtfsURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	fmt.Printf("Downloaded %d bytes\n", len(body))

	if err := extractGTFSZip(body); err != nil {
		return err
	}
	fmt.Println("Done.")
	return nil
}
