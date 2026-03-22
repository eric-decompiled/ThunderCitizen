// Command buildshapes generates route-shapes.json from GTFS shapes.txt and trips.txt.
// Each route gets all its shape variants with full road-following geometry.
//
// Usage:
//
//	go run ./cmd/buildshapes
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	gtfsDir = "static/transit/gtfs"
	outFile = "static/transit/route-shapes.json"
)

type shapePoint struct {
	Lat float64
	Lon float64
	Seq int
}

type routeShape struct {
	RouteID     string       `json:"route_id"`
	Coordinates [][2]float64 `json:"coordinates"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Read shapes.txt → shape_id → sorted points
	shapes, err := readShapes()
	if err != nil {
		return fmt.Errorf("reading shapes: %w", err)
	}
	fmt.Printf("Loaded %d shapes\n", len(shapes))

	// 2. Read trips.txt → route_id → set of shape_ids
	routeShapes, err := readTrips()
	if err != nil {
		return fmt.Errorf("reading trips: %w", err)
	}
	fmt.Printf("Loaded %d routes with shapes\n", len(routeShapes))

	// 3. Build output: include all shape variants per route
	var result []routeShape
	for routeID, shapeIDs := range routeShapes {
		sids := make([]string, 0, len(shapeIDs))
		for sid := range shapeIDs {
			sids = append(sids, sid)
		}
		sort.Strings(sids)

		for _, sid := range sids {
			pts, ok := shapes[sid]
			if !ok || len(pts) == 0 {
				continue
			}

			sort.Slice(pts, func(i, j int) bool { return pts[i].Seq < pts[j].Seq })

			coords := make([][2]float64, len(pts))
			for i, p := range pts {
				coords[i] = [2]float64{p.Lat, p.Lon}
			}

			result = append(result, routeShape{RouteID: routeID, Coordinates: coords})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].RouteID < result[j].RouteID })

	// 4. Write JSON
	f, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("creating output: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	fmt.Printf("Wrote %d route shapes to %s\n", len(result), outFile)
	return nil
}

func readShapes() (map[string][]shapePoint, error) {
	f, err := os.Open(gtfsDir + "/shapes.txt")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	cols := indexCols(header)

	result := make(map[string][]shapePoint)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		shapeID := getCol(record, cols, "shape_id")
		lat, _ := strconv.ParseFloat(getCol(record, cols, "shape_pt_lat"), 64)
		lon, _ := strconv.ParseFloat(getCol(record, cols, "shape_pt_lon"), 64)
		seq, _ := strconv.Atoi(getCol(record, cols, "shape_pt_sequence"))

		result[shapeID] = append(result[shapeID], shapePoint{Lat: lat, Lon: lon, Seq: seq})
	}
	return result, nil
}

func readTrips() (map[string]map[string]bool, error) {
	f, err := os.Open(gtfsDir + "/trips.txt")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	cols := indexCols(header)

	result := make(map[string]map[string]bool)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		routeID := getCol(record, cols, "route_id")
		shapeID := getCol(record, cols, "shape_id")
		if routeID == "" || shapeID == "" {
			continue
		}

		if result[routeID] == nil {
			result[routeID] = make(map[string]bool)
		}
		result[routeID][shapeID] = true
	}
	return result, nil
}

func indexCols(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, col := range header {
		col = strings.TrimSpace(col)
		if i == 0 {
			col = strings.TrimPrefix(col, "\xef\xbb\xbf")
		}
		m[col] = i
	}
	return m
}

func getCol(record []string, cols map[string]int, name string) string {
	idx, ok := cols[name]
	if !ok || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}
