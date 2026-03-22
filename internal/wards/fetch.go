// Package wards fetches Thunder Bay ward boundary data from the
// Open North Represent API and writes a single GeoJSON FeatureCollection.
package wards

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"thundercitizen/internal/fetch"
)

const (
	listURL = "https://represent.opennorth.ca/boundaries/thunder-bay-wards/?format=json"
	outPath = "static/councillors/thunder-bay-wards.geojson"
)

// API response types
type boundaryList struct {
	Objects []boundaryMeta `json:"objects"`
}

type boundaryMeta struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// GeoJSON output types
type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type feature struct {
	Type       string          `json:"type"`
	Properties props           `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type props struct {
	Name string `json:"name"`
}

// DiscoverSources fetches the Open North list endpoint to enumerate the
// per-ward shape URLs that would be downloaded. The list call IS made
// (it's the cheap discovery step), but no shape geometries are downloaded.
func DiscoverSources(ctx context.Context) ([]fetch.Source, error) {
	list, err := fetchList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]fetch.Source, 0, len(list.Objects)+1)
	out = append(out, fetch.Source{Label: "Ward list", URL: listURL})
	for _, w := range list.Objects {
		out = append(out, fetch.Source{
			Label: "Ward shape: " + w.Name,
			URL:   shapeURL(w.URL),
		})
	}
	return out, nil
}

// Fetch downloads each ward boundary shape and writes the assembled
// FeatureCollection to static/councillors/thunder-bay-wards.geojson.
func Fetch(ctx context.Context) error {
	fmt.Println("Fetching ward list...")
	list, err := fetchList(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Found %d wards\n", len(list.Objects))

	fc := featureCollection{
		Type:     "FeatureCollection",
		Features: make([]feature, 0, len(list.Objects)),
	}

	for _, ward := range list.Objects {
		fmt.Printf("  Fetching boundary: %s\n", ward.Name)
		geometry, err := fetchShape(ctx, ward.URL)
		if err != nil {
			return fmt.Errorf("ward %s: %w", ward.Name, err)
		}
		fc.Features = append(fc.Features, feature{
			Type:       "Feature",
			Properties: props{Name: ward.Name},
			Geometry:   geometry,
		})
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	out, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling geojson: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	fmt.Printf("Wrote %d ward boundaries to %s (%d bytes)\n", len(fc.Features), outPath, len(out))
	return nil
}

func fetchList(ctx context.Context) (*boundaryList, error) {
	body, err := httpGet(ctx, listURL)
	if err != nil {
		return nil, fmt.Errorf("fetching ward list: %w", err)
	}
	var list boundaryList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parsing ward list: %w", err)
	}
	return &list, nil
}

func fetchShape(ctx context.Context, wardPath string) (json.RawMessage, error) {
	body, err := httpGet(ctx, shapeURL(wardPath))
	if err != nil {
		return nil, fmt.Errorf("fetching shape: %w", err)
	}
	var geometry json.RawMessage
	if err := json.Unmarshal(body, &geometry); err != nil {
		return nil, fmt.Errorf("parsing shape: %w", err)
	}
	return geometry, nil
}

func shapeURL(wardPath string) string {
	return "https://represent.opennorth.ca" + wardPath + "simple_shape"
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
