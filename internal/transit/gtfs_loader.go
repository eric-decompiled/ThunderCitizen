package transit

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/logger"
)

var gtfsLog = logger.New("gtfs_loader")

const (
	gtfsBaseDir = "static/transit/gtfs"
	gtfsURL     = "http://api.nextlift.ca/gtfs.zip"
)

// LoadStaticGTFS loads the GTFS bundle from local CSV files into Postgres in
// two phases:
//
//  1. Tier 1 staging: bulk-load every CSV into the gtfs.* schema inside a
//     single transaction (TRUNCATE + INSERT). The staging tables are loader-
//     private — nothing else in the application reads them.
//
//  2. Tier 2 derive: build the stable application entities (transit.route,
//     transit.stop, transit.route_pattern, transit.route_baseline,
//     transit.trip_catalog, transit.service_calendar, transit.scheduled_stop)
//     from the staging tables inside a second transaction. The transaction
//     boundary makes the entity refresh atomic — readers see the previous
//     entity state until COMMIT, then the new one. If derive fails, the
//     previous Tier 2 stays intact.
//
// The bundle hash short-circuit lives in GTFSRefresher.checkAndReload — by
// the time we get here, we know the bundle is new and we can do a full
// reload from the on-disk CSVs.
func LoadStaticGTFS(ctx context.Context, db *pgxpool.Pool) error {
	gtfsLog.Info("loading GTFS")

	if err := loadStagingGTFS(ctx, db); err != nil {
		return fmt.Errorf("staging: %w", err)
	}

	if err := DeriveTier2(ctx, db); err != nil {
		return fmt.Errorf("derive: %w", err)
	}

	// Update planner statistics on the entity layer. Bulk-loaded heap pages
	// don't trigger autoanalyze, so the planner ends up with stale stats and
	// picks bad plans for the per-band metric queries on the next read.
	for _, t := range []string{
		"transit.route", "transit.stop", "transit.route_pattern",
		"transit.route_pattern_stop", "transit.route_baseline",
		"transit.trip_catalog", "transit.service_calendar",
		"transit.scheduled_stop",
	} {
		if _, err := db.Exec(ctx, "ANALYZE "+t); err != nil {
			gtfsLog.Warn("analyze failed", "table", t, "err", err)
		}
	}

	// Notify subscribers (recorder trip cache, metric cache) that a fresh
	// Tier 2 is now visible. Subscribers reload their in-memory state.
	NotifyGTFSReloaded()

	return nil
}

// EnsureStaticGTFS checks that the GTFS staging directory exists locally.
func EnsureStaticGTFS() error {
	if err := os.MkdirAll(gtfsBaseDir, 0o755); err != nil {
		return fmt.Errorf("creating GTFS directory: %w", err)
	}
	return nil
}

// loadStagingGTFS bulk-loads each GTFS CSV file into the matching gtfs.* table.
// Wraps the entire load in a single transaction so a partial parse failure
// rolls everything back to the previous good state.
func loadStagingGTFS(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// TRUNCATE every staging table at once. CASCADE isn't needed because
	// nothing references gtfs.* via FK — it's loader-private.
	if _, err := tx.Exec(ctx, `
		TRUNCATE
			gtfs.routes, gtfs.stops, gtfs.trips, gtfs.stop_times,
			gtfs.calendar, gtfs.calendar_dates, gtfs.shapes,
			gtfs.transfers, gtfs.feed_info
		RESTART IDENTITY
	`); err != nil {
		return fmt.Errorf("truncate gtfs.*: %w", err)
	}

	loaders := []struct {
		file string
		load func(context.Context, pgx.Tx) error
	}{
		{"feed_info.txt", loadFeedInfo},
		{"routes.txt", loadRoutes},
		{"stops.txt", loadStops},
		{"trips.txt", loadTrips},
		{"stop_times.txt", loadStopTimes},
		{"calendar.txt", loadCalendar},
		{"calendar_dates.txt", loadCalendarDates},
		{"shapes.txt", loadShapes},
		{"transfers.txt", loadTransfers},
	}

	for _, l := range loaders {
		path := gtfsBaseDir + "/" + l.file
		if _, err := os.Stat(path); err != nil {
			gtfsLog.Info("file not found, skipping", "file", l.file)
			continue
		}
		if err := l.load(ctx, tx); err != nil {
			return fmt.Errorf("loading %s: %w", l.file, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit staging: %w", err)
	}
	return nil
}

// --- per-file CSV loaders (write into gtfs.*) ---

func loadFeedInfo(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/feed_info.txt")
	if err != nil || len(records) == 0 {
		return err
	}
	count := 0
	for _, r := range records {
		startDate := parseGTFSDate(r["feed_start_date"])
		endDate := parseGTFSDate(r["feed_end_date"])
		if _, err := tx.Exec(ctx, `
			INSERT INTO gtfs.feed_info
				(feed_publisher_name, feed_publisher_url, feed_lang,
				 feed_start_date, feed_end_date, feed_version)
			VALUES ($1, $2, $3, $4, $5, $6)
		`,
			r["feed_publisher_name"], r["feed_publisher_url"], r["feed_lang"],
			startDate, endDate, r["feed_version"],
		); err != nil {
			return fmt.Errorf("insert feed_info: %w", err)
		}
		count++
	}
	gtfsLog.Info("loaded feed_info", "count", count)
	return nil
}

func loadRoutes(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/routes.txt")
	if err != nil {
		return err
	}
	for _, r := range records {
		color := r["route_color"]
		textColor := r["route_text_color"]
		if color != "" && !strings.HasPrefix(color, "#") {
			color = "#" + color
		}
		if textColor != "" && !strings.HasPrefix(textColor, "#") {
			textColor = "#" + textColor
		}
		routeType, _ := strconv.Atoi(r["route_type"])
		if _, err := tx.Exec(ctx, `
			INSERT INTO gtfs.routes
				(route_id, short_name, long_name, route_type, color, text_color)
			VALUES ($1, $2, $3, $4, $5, $6)
		`,
			r["route_id"], nilIfEmpty(r["route_short_name"]),
			nilIfEmpty(r["route_long_name"]), routeType,
			nilIfEmpty(color), nilIfEmpty(textColor),
		); err != nil {
			return fmt.Errorf("insert route %s: %w", r["route_id"], err)
		}
	}
	gtfsLog.Info("loaded routes", "count", len(records))
	return nil
}

func loadStops(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/stops.txt")
	if err != nil {
		return err
	}
	for _, r := range records {
		lat, _ := strconv.ParseFloat(r["stop_lat"], 64)
		lon, _ := strconv.ParseFloat(r["stop_lon"], 64)
		wheelchair, _ := strconv.Atoi(r["wheelchair_boarding"])
		if _, err := tx.Exec(ctx, `
			INSERT INTO gtfs.stops
				(stop_id, stop_name, stop_code, latitude, longitude,
				 wheelchair, parent_station)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
			r["stop_id"], nilIfEmpty(r["stop_name"]), nilIfEmpty(r["stop_code"]),
			lat, lon, wheelchair, nilIfEmpty(r["parent_station"]),
		); err != nil {
			return fmt.Errorf("insert stop %s: %w", r["stop_id"], err)
		}
	}
	gtfsLog.Info("loaded stops", "count", len(records))
	return nil
}

func loadTrips(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/trips.txt")
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, r := range records {
		dirID, _ := strconv.Atoi(r["direction_id"])
		batch.Queue(`
			INSERT INTO gtfs.trips
				(trip_id, route_id, service_id, headsign, direction_id, shape_id, block_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
			r["trip_id"], r["route_id"], r["service_id"],
			nilIfEmpty(r["trip_headsign"]), dirID,
			nilIfEmpty(r["shape_id"]), nilIfEmpty(r["block_id"]),
		)
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("insert trips: %w", err)
	}
	gtfsLog.Info("loaded trips", "count", len(records))
	return nil
}

func loadStopTimes(ctx context.Context, tx pgx.Tx) error {
	// stop_times.txt is the largest file (~118K rows). Use COPY FROM for
	// throughput — batched INSERT takes minutes, COPY is seconds.
	f, err := os.Open(gtfsBaseDir + "/stop_times.txt")
	if err != nil {
		return err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("reading header: %w", err)
	}
	cols := indexColumns(header)

	var rows [][]any
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading row: %w", err)
		}
		seq, _ := strconv.Atoi(getCol(record, cols, "stop_sequence"))
		timepoint := getCol(record, cols, "timepoint") == "1"
		rows = append(rows, []any{
			getCol(record, cols, "trip_id"),
			seq,
			getCol(record, cols, "stop_id"),
			nilIfEmpty(getCol(record, cols, "arrival_time")),
			nilIfEmpty(getCol(record, cols, "departure_time")),
			timepoint,
		})
	}

	n, err := tx.CopyFrom(ctx,
		pgx.Identifier{"gtfs", "stop_times"},
		[]string{"trip_id", "stop_sequence", "stop_id", "arrival_time", "departure_time", "timepoint"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy stop_times: %w", err)
	}
	gtfsLog.Info("loaded stop_times", "count", n)
	return nil
}

func loadCalendar(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/calendar.txt")
	if err != nil {
		return err
	}
	for _, r := range records {
		if _, err := tx.Exec(ctx, `
			INSERT INTO gtfs.calendar
				(service_id, monday, tuesday, wednesday, thursday, friday,
				 saturday, sunday, start_date, end_date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`,
			r["service_id"],
			r["monday"] == "1", r["tuesday"] == "1", r["wednesday"] == "1",
			r["thursday"] == "1", r["friday"] == "1", r["saturday"] == "1",
			r["sunday"] == "1",
			parseGTFSDate(r["start_date"]), parseGTFSDate(r["end_date"]),
		); err != nil {
			return fmt.Errorf("insert calendar %s: %w", r["service_id"], err)
		}
	}
	gtfsLog.Info("loaded calendar", "count", len(records))
	return nil
}

func loadCalendarDates(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/calendar_dates.txt")
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, r := range records {
		exType, _ := strconv.Atoi(r["exception_type"])
		batch.Queue(`
			INSERT INTO gtfs.calendar_dates (service_id, date, exception_type)
			VALUES ($1, $2, $3)
		`,
			r["service_id"], parseGTFSDate(r["date"]), exType,
		)
	}
	if err := execTxBatch(ctx, tx, batch); err != nil {
		return fmt.Errorf("insert calendar_dates: %w", err)
	}
	gtfsLog.Info("loaded calendar_dates", "count", len(records))
	return nil
}

func loadShapes(ctx context.Context, tx pgx.Tx) error {
	f, err := os.Open(gtfsBaseDir + "/shapes.txt")
	if err != nil {
		return err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("reading header: %w", err)
	}
	cols := indexColumns(header)

	var rows [][]any
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading row: %w", err)
		}
		seq, _ := strconv.Atoi(getCol(record, cols, "shape_pt_sequence"))
		lat, _ := strconv.ParseFloat(getCol(record, cols, "shape_pt_lat"), 64)
		lon, _ := strconv.ParseFloat(getCol(record, cols, "shape_pt_lon"), 64)
		dist, _ := strconv.ParseFloat(getCol(record, cols, "shape_dist_traveled"), 64)
		rows = append(rows, []any{
			getCol(record, cols, "shape_id"),
			seq, lat, lon, dist,
		})
	}

	n, err := tx.CopyFrom(ctx,
		pgx.Identifier{"gtfs", "shapes"},
		[]string{"shape_id", "shape_pt_sequence", "shape_pt_lat", "shape_pt_lon", "shape_dist_traveled"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy shapes: %w", err)
	}
	gtfsLog.Info("loaded shapes", "count", n)
	return nil
}

func loadTransfers(ctx context.Context, tx pgx.Tx) error {
	records, err := readCSV(gtfsBaseDir + "/transfers.txt")
	if err != nil {
		return err
	}
	for _, r := range records {
		transferType, _ := strconv.Atoi(r["transfer_type"])
		minTime := nilIfEmpty(r["min_transfer_time"])
		var minTimeInt *int
		if minTime != nil {
			if v, err := strconv.Atoi(*minTime); err == nil {
				minTimeInt = &v
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gtfs.transfers
				(from_stop_id, to_stop_id, transfer_type, min_transfer_time)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (from_stop_id, to_stop_id) DO NOTHING
		`,
			r["from_stop_id"], r["to_stop_id"], transferType, minTimeInt,
		); err != nil {
			return fmt.Errorf("insert transfer: %w", err)
		}
	}
	gtfsLog.Info("loaded transfers", "count", len(records))
	return nil
}

// --- GTFS background refresher ---

// GTFSRefresher periodically checks for GTFS static feed updates and reloads
// when changed.
type GTFSRefresher struct {
	db   *pgxpool.Pool
	repo *Repo
	mu   sync.Mutex
}

// NewGTFSRefresher creates a new GTFS refresher.
func NewGTFSRefresher(db *pgxpool.Pool) *GTFSRefresher {
	return &GTFSRefresher{db: db, repo: NewRepo(db)}
}

// Start begins the background GTFS refresh loop.
func (g *GTFSRefresher) Start(ctx context.Context) {
	go g.pollLoop(ctx)
}

func (g *GTFSRefresher) pollLoop(ctx context.Context) {
	_ = g.CheckAndReload(ctx)
	for {
		select {
		case <-time.After(GTFSRefreshInterval):
			_ = g.CheckAndReload(ctx)
		case <-ctx.Done():
			gtfsLog.Info("GTFS refresher stopped")
			return
		}
	}
}

// CheckAndReload downloads the latest GTFS zip, hash-checks it against the
// version stored in the DB, and if it changed, extracts + reloads. Returns
// nil on success (including the hash-match fast path) or an error on any
// failure. Errors are logged before being returned so pollLoop callers can
// ignore the return value and still get log coverage.
//
// Safe to call from a one-shot CLI on startup for synchronous bootstrap.
func (g *GTFSRefresher) CheckAndReload(ctx context.Context) error {
	gtfsLog.Info("checking for GTFS updates")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(gtfsURL)
	if err != nil {
		gtfsLog.Error("GTFS download failed", "err", err)
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		gtfsLog.Error("GTFS download HTTP error", "status", resp.StatusCode)
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		gtfsLog.Error("GTFS read failed", "err", err)
		return fmt.Errorf("read: %w", err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	stored := g.repo.GetGTFSVersionHash(ctx)

	if hash == stored {
		gtfsLog.Info("GTFS unchanged", "hash", hash[:12])
		return nil
	}

	gtfsLog.Info("GTFS update detected", "old", truncHash(stored), "new", hash[:12], "size", len(body))

	before := g.snapshotCounts(ctx)

	if err := extractGTFSZip(body); err != nil {
		gtfsLog.Error("GTFS extract failed", "err", err)
		return fmt.Errorf("extract: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if err := LoadStaticGTFS(loadCtx, g.db); err != nil {
		gtfsLog.Error("GTFS reload failed", "err", err)
		return fmt.Errorf("load: %w", err)
	}

	if err := g.repo.SetGTFSVersion(ctx, hash); err != nil {
		gtfsLog.Error("GTFS version save failed", "err", err)
		return fmt.Errorf("save version: %w", err)
	}

	after := g.snapshotCounts(ctx)
	g.logDelta(before, after)
	return nil
}

func extractGTFSZip(data []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}

	if err := os.MkdirAll(gtfsBaseDir, 0o755); err != nil {
		return fmt.Errorf("creating dir: %w", err)
	}

	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		outPath := filepath.Join(gtfsBaseDir, filepath.Base(f.Name))
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening %s: %w", f.Name, err)
		}
		out, err := os.Create(outPath)
		if err != nil {
			rc.Close()
			return fmt.Errorf("creating %s: %w", outPath, err)
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return fmt.Errorf("extracting %s: %w", f.Name, err)
		}
	}
	return nil
}

// --- snapshot / delta logging ---

type gtfsSnapshot struct {
	Trips        int
	StopTimes    int
	Routes       int
	Stops        int
	Services     int
	TripsByRoute map[string]int
}

func (g *GTFSRefresher) snapshotCounts(ctx context.Context) gtfsSnapshot {
	var s gtfsSnapshot
	s.TripsByRoute = make(map[string]int)
	_ = g.db.QueryRow(ctx, "SELECT COUNT(*) FROM transit.trip_catalog").Scan(&s.Trips)
	_ = g.db.QueryRow(ctx, "SELECT COUNT(*) FROM transit.scheduled_stop").Scan(&s.StopTimes)
	_ = g.db.QueryRow(ctx, "SELECT COUNT(*) FROM transit.route").Scan(&s.Routes)
	_ = g.db.QueryRow(ctx, "SELECT COUNT(*) FROM transit.stop").Scan(&s.Stops)
	_ = g.db.QueryRow(ctx, "SELECT COUNT(DISTINCT service_id) FROM transit.service_calendar").Scan(&s.Services)
	rows, err := g.db.Query(ctx, "SELECT route_id, COUNT(*) FROM transit.trip_catalog GROUP BY route_id")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rid string
			var cnt int
			if rows.Scan(&rid, &cnt) == nil {
				s.TripsByRoute[rid] = cnt
			}
		}
	}
	return s
}

func (g *GTFSRefresher) logDelta(before, after gtfsSnapshot) {
	gtfsLog.Info("GTFS reloaded",
		"trips", fmt.Sprintf("%d → %d", before.Trips, after.Trips),
		"stop_times", fmt.Sprintf("%d → %d", before.StopTimes, after.StopTimes),
		"routes", fmt.Sprintf("%d → %d", before.Routes, after.Routes),
		"stops", fmt.Sprintf("%d → %d", before.Stops, after.Stops),
		"services", fmt.Sprintf("%d → %d", before.Services, after.Services),
	)

	allRoutes := make(map[string]bool)
	for r := range before.TripsByRoute {
		allRoutes[r] = true
	}
	for r := range after.TripsByRoute {
		allRoutes[r] = true
	}

	for r := range allRoutes {
		b, a := before.TripsByRoute[r], after.TripsByRoute[r]
		if b == 0 && a > 0 {
			gtfsLog.Warn("new route appeared", "route", r, "trips", a)
		} else if b > 0 && a == 0 {
			gtfsLog.Warn("route disappeared", "route", r, "had_trips", b)
		} else if b != a {
			gtfsLog.Info("route trip change", "route", r, "before", b, "after", a)
		}
	}

	if diff := after.Routes - before.Routes; diff < -2 || diff > 2 {
		gtfsLog.Warn("significant route count change", "before", before.Routes, "after", after.Routes)
	}
}

func truncHash(h string) string {
	if len(h) >= 12 {
		return h[:12]
	}
	if h == "" {
		return "(none)"
	}
	return h
}

// --- helpers ---

// readCSV reads a GTFS CSV file and returns each row as a column→value map.
// Convenience wrapper for the small files; for large files use a streaming
// reader directly so memory stays bounded.
func readCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	cols := indexColumns(header)

	var rows []map[string]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading row: %w", err)
		}
		row := make(map[string]string, len(cols))
		for col, idx := range cols {
			row[col] = getCol(record, cols, col)
			_ = idx
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// execTxBatch sends a pgx batch on a transaction and waits for every result.
// Must be called on a transaction (not a pool) because pgx requires the batch
// results to be fully drained and closed before the transaction can run any
// further commands. The explicit Close() here does that draining.
func execTxBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	if batch.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

func indexColumns(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, col := range header {
		col = strings.TrimSpace(col)
		// Strip UTF-8 BOM from first column if present
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

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseGTFSDate converts a GTFS YYYYMMDD date string to YYYY-MM-DD for
// Postgres. Empty input returns nil.
func parseGTFSDate(s string) *string {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return nil
	}
	out := s[:4] + "-" + s[4:6] + "-" + s[6:]
	return &out
}
