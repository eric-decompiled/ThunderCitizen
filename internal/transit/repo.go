package transit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides database access for transit data.
type Repo struct {
	db *pgxpool.Pool
}

// NewRepo creates a new transit repository.
func NewRepo(db *pgxpool.Pool) *Repo {
	return &Repo{db: db}
}

// Pool returns the underlying connection pool for raw queries.
func (r *Repo) Pool() *pgxpool.Pool {
	return r.db
}

// --- Stops & Routes ---

// AllStops returns every stop with valid coordinates, including route count.
func (r *Repo) AllStops(ctx context.Context) ([]Stop, error) {
	rows, err := r.db.Query(ctx, `
		SELECT s.stop_id, COALESCE(s.name, '') AS stop_name,
		       s.latitude, s.longitude,
		       COALESCE(rc.routes, 0)::INT AS routes,
		       COALESCE(rc.route_ids, ARRAY[]::TEXT[]) AS route_ids,
		       s.is_transfer,
		       s.is_terminal
		FROM transit.stop s
		LEFT JOIN (
			SELECT rps.stop_id,
			       COUNT(DISTINCT rp.route_id) AS routes,
			       ARRAY_AGG(DISTINCT rp.route_id ORDER BY rp.route_id) AS route_ids
			FROM transit.route_pattern_stop rps
			JOIN transit.route_pattern rp USING (pattern_id)
			GROUP BY rps.stop_id
		) rc ON rc.stop_id = s.stop_id
		WHERE s.latitude IS NOT NULL AND s.longitude IS NOT NULL
		  AND s.stop_id NOT IN ('9991')
		ORDER BY s.stop_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stops []Stop
	for rows.Next() {
		var s Stop
		if err := rows.Scan(&s.StopID, &s.StopName, &s.Latitude, &s.Longitude, &s.Routes, &s.RouteIDs, &s.Transfer, &s.IsTerminal); err != nil {
			return nil, err
		}
		stops = append(stops, s)
	}
	return stops, rows.Err()
}

// RouteTimepoint is an official schedule-adherence stop for a route direction.
type RouteTimepoint struct {
	RouteID  string `json:"route_id"`
	Headsign string `json:"headsign"`
	StopID   string `json:"stop_id"`
	StopName string `json:"stop_name"`
	Sequence int    `json:"sequence"`
}

// RouteTimepoints returns the official time point stops for a route, ordered by direction and sequence.
func (r *Repo) RouteTimepoints(ctx context.Context, routeID string) ([]RouteTimepoint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.route_id, rp.headsign, rps.stop_id,
		       COALESCE(s.name, rps.stop_id) AS stop_name, rps.sequence
		FROM transit.route_pattern rp
		JOIN transit.route_pattern_stop rps USING (pattern_id)
		JOIN transit.stop s ON s.stop_id = rps.stop_id
		WHERE rp.route_id = $1 AND rps.is_timepoint = true
		ORDER BY rp.headsign, rps.sequence
	`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tps []RouteTimepoint
	for rows.Next() {
		var tp RouteTimepoint
		if err := rows.Scan(&tp.RouteID, &tp.Headsign, &tp.StopID, &tp.StopName, &tp.Sequence); err != nil {
			return nil, err
		}
		tps = append(tps, tp)
	}
	return tps, rows.Err()
}

// AllRouteTimepoints returns time points for all routes.
func (r *Repo) AllRouteTimepoints(ctx context.Context) (map[string][]RouteTimepoint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.route_id, rp.headsign, rps.stop_id,
		       COALESCE(s.name, rps.stop_id) AS stop_name, rps.sequence
		FROM transit.route_pattern rp
		JOIN transit.route_pattern_stop rps USING (pattern_id)
		JOIN transit.stop s ON s.stop_id = rps.stop_id
		WHERE rps.is_timepoint = true
		ORDER BY rp.route_id, rp.headsign, rps.sequence
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string][]RouteTimepoint{}
	for rows.Next() {
		var tp RouteTimepoint
		if err := rows.Scan(&tp.RouteID, &tp.Headsign, &tp.StopID, &tp.StopName, &tp.Sequence); err != nil {
			return nil, err
		}
		result[tp.RouteID] = append(result[tp.RouteID], tp)
	}
	return result, rows.Err()
}

// RouteDisplay holds display info returned by RouteDisplayInfo.
type RouteDisplay struct {
	RouteID   string
	ShortName string
	Color     string
	LongName  string
}

// RouteDisplayInfo returns display metadata for all routes. No ORDER BY —
// every caller keys these into a map by route_id, so sort order is never
// observed. A regex-based sort here was visibly in the stop-predictions hot
// path at ~70ms per call.
func (r *Repo) RouteDisplayInfo(ctx context.Context) ([]RouteDisplay, error) {
	rows, err := r.db.Query(ctx, `
		SELECT route_id, COALESCE(NULLIF(short_name, ''), route_id) AS short_name,
		       color, long_name
		FROM transit.route`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RouteDisplay
	for rows.Next() {
		var rd RouteDisplay
		if err := rows.Scan(&rd.RouteID, &rd.ShortName, &rd.Color, &rd.LongName); err != nil {
			return nil, err
		}
		result = append(result, rd)
	}
	return result, rows.Err()
}

// RouteMetaAPI holds full route metadata for the frontend including terminals.
type RouteMetaAPI struct {
	RouteID   string   `json:"route_id"`
	Name      string   `json:"name"`
	Color     string   `json:"color"`
	TextColor string   `json:"text_color"`
	Terminals []string `json:"terminals"`
}

// AllRouteMeta returns display metadata and terminal stops for routes with
// scheduled service in the given date range (7-day trailing window).
func (r *Repo) AllRouteMeta(ctx context.Context, from, to time.Time) ([]RouteMetaAPI, error) {
	rows, err := r.db.Query(ctx, `
		WITH active_routes AS (
			SELECT DISTINCT tc.route_id
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE sc.date BETWEEN $1 AND $2
		),
		route_ends AS (
			SELECT rp.route_id, s.name AS stop_name,
			       ROW_NUMBER() OVER (PARTITION BY rp.pattern_id ORDER BY rps.sequence ASC) AS first_rn,
			       ROW_NUMBER() OVER (PARTITION BY rp.pattern_id ORDER BY rps.sequence DESC) AS last_rn
			FROM transit.route_pattern rp
			JOIN transit.route_pattern_stop rps USING (pattern_id)
			JOIN transit.stop s ON s.stop_id = rps.stop_id
			WHERE rps.is_timepoint = true
		),
		terminals AS (
			SELECT route_id, ARRAY_AGG(DISTINCT stop_name) AS names
			FROM route_ends
			WHERE first_rn = 1 OR last_rn = 1
			GROUP BY route_id
		)
		SELECT r.route_id,
		       COALESCE(NULLIF(r.long_name, ''), NULLIF(r.short_name, ''), r.route_id) AS name,
		       r.color,
		       r.text_color,
		       COALESCE(t.names, ARRAY[]::TEXT[]) AS terminals
		FROM transit.route r
		JOIN active_routes ar ON ar.route_id = r.route_id
		LEFT JOIN terminals t ON t.route_id = r.route_id
		ORDER BY
		  CAST(NULLIF(REGEXP_REPLACE(r.route_id, '[^0-9]', '', 'g'), '') AS INT) NULLS LAST,
		  REGEXP_REPLACE(r.route_id, '[0-9]', '', 'g')`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RouteMetaAPI
	for rows.Next() {
		var rm RouteMetaAPI
		if err := rows.Scan(&rm.RouteID, &rm.Name, &rm.Color, &rm.TextColor, &rm.Terminals); err != nil {
			return nil, err
		}
		result = append(result, rm)
	}
	return result, rows.Err()
}

// GetRoute returns display info for a single route.
func (r *Repo) GetRoute(ctx context.Context, routeID string) (*RouteInfo, error) {
	var ri RouteInfo
	err := r.db.QueryRow(ctx, `
		SELECT route_id, short_name, long_name, color, text_color
		FROM transit.route WHERE route_id = $1`, routeID).
		Scan(&ri.RouteID, &ri.ShortName, &ri.LongName, &ri.Color, &ri.TextColor)
	if err != nil {
		return nil, err
	}
	return &ri, nil
}

// --- Spatial Queries ---

// StopWithDistance is a stop with its distance from a query point.
type StopWithDistance struct {
	StopID    string  `json:"stop_id"`
	StopName  string  `json:"stop_name"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	DistanceM float64 `json:"distance_m"`
}

// NearestStops returns the N closest stops to the given lat/lon,
// using the PostGIS spatial index for efficient nearest-neighbor search.
func (r *Repo) NearestStops(ctx context.Context, lat, lon float64, limit int) ([]StopWithDistance, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := r.db.Query(ctx, `
		SELECT stop_id, name AS stop_name, latitude, longitude,
		       ST_Distance(geog, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM transit.stop
		WHERE geog IS NOT NULL
		ORDER BY geog <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
		LIMIT $3`,
		lon, lat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StopWithDistance
	for rows.Next() {
		var s StopWithDistance
		if err := rows.Scan(&s.StopID, &s.StopName, &s.Latitude, &s.Longitude, &s.DistanceM); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// VehicleDistance describes a vehicle's proximity to a stop.
type VehicleDistance struct {
	VehicleID     string    `json:"vehicle_id"`
	RouteID       string    `json:"route_id,omitempty"`
	Latitude      float64   `json:"lat"`
	Longitude     float64   `json:"lon"`
	DistanceM     float64   `json:"distance_m"`
	FeedTimestamp time.Time `json:"feed_timestamp"`
}

// VehicleDistanceToStop returns the distance in meters from the latest position
// of a vehicle to a stop, using PostGIS geography distance.
func (r *Repo) VehicleDistanceToStop(ctx context.Context, vehicleID, stopID string) (*VehicleDistance, error) {
	var vd VehicleDistance
	err := r.db.QueryRow(ctx, `
		SELECT evp.vehicle_id, COALESCE(evp.route_id, ''),
		       evp.latitude, evp.longitude,
		       ST_Distance(evp.geog, s.geog) AS distance_m,
		       evp.feed_timestamp
		FROM transit.vehicle_position evp
		JOIN transit.stop s ON s.stop_id = $2
		WHERE evp.vehicle_id = $1
		  AND evp.geog IS NOT NULL
		  AND s.geog IS NOT NULL
		ORDER BY evp.feed_timestamp DESC
		LIMIT 1`,
		vehicleID, stopID).
		Scan(&vd.VehicleID, &vd.RouteID, &vd.Latitude, &vd.Longitude, &vd.DistanceM, &vd.FeedTimestamp)
	if err != nil {
		return nil, err
	}
	return &vd, nil
}

// --- Alerts ---

// CurrentAlerts returns alerts from the most recent feed poll.
func (r *Repo) CurrentAlerts(ctx context.Context) ([]ActiveAlert, error) {
	rows, err := r.db.Query(ctx, `
		SELECT alert_id, cause, effect, header, description,
		       severity_level, affected_routes, affected_stops
		FROM transit.alert
		WHERE feed_timestamp = (SELECT MAX(feed_timestamp) FROM transit.alert)
		ORDER BY alert_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ActiveAlert
	for rows.Next() {
		var a ActiveAlert
		if err := rows.Scan(
			&a.AlertID, &a.Cause, &a.Effect, &a.Header, &a.Description,
			&a.SeverityLevel, &a.AffectedRoutes, &a.AffectedStops,
		); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// CancelledRoutes returns route IDs with cancellations in the most recent feed poll.
func (r *Repo) CancelledRoutes(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT route_id FROM transit.cancellation
		WHERE feed_timestamp = (SELECT MAX(feed_timestamp) FROM transit.cancellation)
		ORDER BY route_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var routeID string
		if err := rows.Scan(&routeID); err != nil {
			return nil, err
		}
		result = append(result, routeID)
	}
	return result, rows.Err()
}

// --- Snapshots ---

// DayPercentiles returns delay percentile curves for the last 24 hours,
// bucketed into 30-minute intervals.
func (r *Repo) DayPercentiles(ctx context.Context) ([]DelayPercentileBucket, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			(date_trunc('hour', last_updated) + INTERVAL '30 min' * FLOOR(EXTRACT(MINUTE FROM last_updated) / 30))::TIMESTAMPTZ AS bucket_time,
			PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY COALESCE(arrival_delay, departure_delay))::FLOAT AS p50,
			PERCENTILE_CONT(0.90) WITHIN GROUP (ORDER BY COALESCE(arrival_delay, departure_delay))::FLOAT AS p90,
			PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY COALESCE(arrival_delay, departure_delay))::FLOAT AS p99,
			PERCENTILE_CONT(0.999) WITHIN GROUP (ORDER BY COALESCE(arrival_delay, departure_delay))::FLOAT AS p999,
			COUNT(*)::INT AS count
		FROM transit.stop_delay
		WHERE last_updated >= NOW() - INTERVAL '24 hours'
		GROUP BY bucket_time
		HAVING COUNT(*) >= 5
		ORDER BY bucket_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DelayPercentileBucket
	for rows.Next() {
		var b DelayPercentileBucket
		if err := rows.Scan(&b.BucketTime, &b.P50, &b.P90, &b.P99, &b.P999, &b.Count); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// DaySnapshots derives 5-minute system snapshots from raw events for the last 24 hours.
func (r *Repo) DaySnapshots(ctx context.Context) ([]TransitSnapshot, error) {
	rows, err := r.db.Query(ctx, `
		WITH buckets AS (
			SELECT generate_series(
				date_trunc('minute', NOW() - INTERVAL '24 hours') - (EXTRACT(MINUTE FROM NOW())::INT % 5) * INTERVAL '1 minute',
				date_trunc('minute', NOW()),
				'5 minutes'::interval
			) AS t
		),
		vehicle_stats AS (
			SELECT date_trunc('minute', feed_timestamp) - (EXTRACT(MINUTE FROM feed_timestamp)::INT % 5) * INTERVAL '1 minute' AS bucket,
				COUNT(DISTINCT vehicle_id) AS active_vehicles,
				COUNT(DISTINCT route_id) AS active_routes
			FROM transit.vehicle_position
			WHERE feed_timestamp >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		),
		delay_stats AS (
			SELECT date_trunc('minute', last_updated) - (EXTRACT(MINUTE FROM last_updated)::INT % 5) * INTERVAL '1 minute' AS bucket,
				COUNT(*) AS measurement_count,
				AVG(COALESCE(arrival_delay, departure_delay))::REAL AS avg_delay,
				COUNT(CASE WHEN ABS(COALESCE(arrival_delay, departure_delay)) <= 60 THEN 1 END) AS on_time,
				COUNT(CASE WHEN COALESCE(arrival_delay, departure_delay) > 60 THEN 1 END) AS late,
				COUNT(CASE WHEN COALESCE(arrival_delay, departure_delay) < -60 THEN 1 END) AS early
			FROM transit.stop_delay
			WHERE last_updated >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		),
		alert_stats AS (
			SELECT date_trunc('minute', feed_timestamp) - (EXTRACT(MINUTE FROM feed_timestamp)::INT % 5) * INTERVAL '1 minute' AS bucket,
				COUNT(DISTINCT alert_id) AS alert_count
			FROM transit.alert
			WHERE feed_timestamp >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		),
		cancel_stats AS (
			SELECT date_trunc('minute', feed_timestamp) - (EXTRACT(MINUTE FROM feed_timestamp)::INT % 5) * INTERVAL '1 minute' AS bucket,
				COUNT(DISTINCT trip_id) AS cancellation_count
			FROM transit.cancellation
			WHERE feed_timestamp >= NOW() - INTERVAL '24 hours'
			GROUP BY 1
		)
		SELECT
			b.t,
			COALESCE(v.active_vehicles, 0)::INT,
			COALESCE(v.active_routes, 0)::INT,
			CASE WHEN COALESCE(d.measurement_count, 0) > 0
				THEN (d.on_time * 100.0 / d.measurement_count)::REAL ELSE 0 END,
			COALESCE(d.avg_delay, 0)::REAL,
			COALESCE(d.late, 0)::INT,
			COALESCE(d.early, 0)::INT,
			COALESCE(d.measurement_count, 0)::INT,
			COALESCE(a.alert_count, 0)::INT,
			COALESCE(c.cancellation_count, 0)::INT
		FROM buckets b
		LEFT JOIN vehicle_stats v ON v.bucket = b.t
		LEFT JOIN delay_stats d ON d.bucket = b.t
		LEFT JOIN alert_stats a ON a.bucket = b.t
		LEFT JOIN cancel_stats c ON c.bucket = b.t
		WHERE COALESCE(v.active_vehicles, 0) > 0
		   OR COALESCE(d.measurement_count, 0) > 0
		ORDER BY b.t`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TransitSnapshot
	for rows.Next() {
		var s TransitSnapshot
		if err := rows.Scan(
			&s.CapturedAt, &s.ActiveVehicles, &s.ActiveRoutes,
			&s.OnTimePct, &s.AvgDelaySeconds,
			&s.LateCount, &s.EarlyCount, &s.MeasurementCount,
			&s.AlertCount, &s.Cancellations,
		); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// WeekSummary derives daily aggregates from raw events for the last 7 days.
func (r *Repo) WeekSummary(ctx context.Context) ([]DaySummary, error) {
	rows, err := r.db.Query(ctx, `
		WITH delay_days AS (
			SELECT DATE(last_updated) AS day,
				COUNT(*) AS measurements,
				COUNT(CASE WHEN ABS(COALESCE(arrival_delay, departure_delay)) <= 60 THEN 1 END) AS on_time,
				AVG(COALESCE(arrival_delay, departure_delay))::REAL AS avg_delay
			FROM transit.stop_delay
			WHERE last_updated >= NOW() - INTERVAL '7 days'
			GROUP BY DATE(last_updated)
		),
		cancel_days AS (
			SELECT DATE(feed_timestamp) AS day,
				COUNT(DISTINCT trip_id) AS cancellations
			FROM transit.cancellation
			WHERE feed_timestamp >= NOW() - INTERVAL '7 days'
			GROUP BY DATE(feed_timestamp)
		)
		SELECT d.day,
			CASE WHEN d.measurements > 0
				THEN (d.on_time * 100.0 / d.measurements)::REAL ELSE 0 END AS avg_on_time,
			COALESCE(d.avg_delay, 0)::REAL,
			COALESCE(c.cancellations, 0)::INT
		FROM delay_days d
		LEFT JOIN cancel_days c ON c.day = d.day
		WHERE d.measurements > 0
		ORDER BY d.day`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DaySummary
	for rows.Next() {
		var s DaySummary
		if err := rows.Scan(&s.Date, &s.AvgOnTime, &s.AvgDelay, &s.Cancellations); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- Feed State ---

// GetFeedLastTimestamp returns the last processed timestamp for a feed type.
func (r *Repo) GetFeedLastTimestamp(ctx context.Context, feedType string) (time.Time, error) {
	var t time.Time
	err := r.db.QueryRow(ctx, `
		SELECT last_timestamp FROM transit.feed_state WHERE feed_type = $1`, feedType).Scan(&t)
	return t, err
}

// UpsertFeedState updates the feed state with a new timestamp.
func (r *Repo) UpsertFeedState(ctx context.Context, feedType string, lastTimestamp time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO transit.feed_state (feed_type, last_timestamp, last_fetched_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (feed_type) DO UPDATE SET
		  last_timestamp = EXCLUDED.last_timestamp,
		  last_fetched_at = NOW()`, feedType, lastTimestamp)
	return err
}

// InsertFeedGap records a gap in feed delivery.
func (r *Repo) InsertFeedGap(ctx context.Context, feedType string, gapStart, gapEnd time.Time, expectedIntervalSec, actualGapSec int32) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO transit.feed_gap (feed_type, gap_start, gap_end, expected_interval_seconds, actual_gap_seconds)
		VALUES ($1, $2, $3, $4, $5)`, feedType, gapStart, gapEnd, expectedIntervalSec, actualGapSec)
	return err
}

// GetGTFSVersionHash returns the stored hash for the static GTFS feed, or "" if none.
func (r *Repo) GetGTFSVersionHash(ctx context.Context) string {
	var hash *string
	_ = r.db.QueryRow(ctx, `
		SELECT version_hash FROM transit.feed_state WHERE feed_type = 'gtfs_static'`).Scan(&hash)
	if hash == nil {
		return ""
	}
	return *hash
}

// SetGTFSVersion updates the stored GTFS version hash and timestamp.
func (r *Repo) SetGTFSVersion(ctx context.Context, hash string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO transit.feed_state (feed_type, last_timestamp, last_fetched_at, version_hash)
		VALUES ('gtfs_static', NOW(), NOW(), $1)
		ON CONFLICT (feed_type) DO UPDATE SET
		  last_timestamp = NOW(),
		  last_fetched_at = NOW(),
		  version_hash = $1`, hash)
	return err
}

// --- Trip Planner ---

// CurrentCancelledTripIDs returns trip IDs from the most recent cancellation feed.
func (r *Repo) CurrentCancelledTripIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.Query(ctx, `
		SELECT trip_id FROM transit.cancellation
		WHERE feed_timestamp = (SELECT MAX(feed_timestamp) FROM transit.cancellation)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

// TimetableForDate loads all trips and stop_times for services active on the
// given date, grouped into RaptorTrip structs for the RAPTOR algorithm.
func (r *Repo) TimetableForDate(ctx context.Context, date time.Time) ([]*RaptorTrip, error) {
	rows, err := r.db.Query(ctx, `
		SELECT tc.trip_id, tc.route_id, tc.headsign,
		       ss.stop_id, ss.stop_sequence,
		       COALESCE(ss.scheduled_arrival, ss.scheduled_departure),
		       COALESCE(ss.scheduled_departure, ss.scheduled_arrival)
		FROM transit.trip_catalog tc
		JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
		JOIN transit.scheduled_stop ss ON ss.trip_id = tc.trip_id
		WHERE sc.date = $1
		  AND ss.scheduled_departure IS NOT NULL
		ORDER BY tc.trip_id, ss.stop_sequence`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trips []*RaptorTrip
	var current *RaptorTrip

	for rows.Next() {
		var tripID, routeID, headsign, stopID, arrTime, depTime string
		var seq int
		if err := rows.Scan(&tripID, &routeID, &headsign, &stopID, &seq, &arrTime, &depTime); err != nil {
			return nil, err
		}

		if current == nil || current.TripID != tripID {
			current = &RaptorTrip{TripID: tripID, RouteID: routeID, Headsign: headsign}
			trips = append(trips, current)
		}

		current.Stops = append(current.Stops, RaptorStopTime{
			StopID:    stopID,
			Arrival:   parseHMS(arrTime),
			Departure: parseHMS(depTime),
		})
	}

	return trips, rows.Err()
}

// FleetSize returns the total number of unique vehicles ever observed.
func (r *Repo) FleetSize(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM transit.vehicle`).Scan(&count)
	return count, err
}

// VehicleStats holds operational data for a single bus from the DB.
type VehicleStats struct {
	VehicleID  string
	FirstSeen  time.Time
	LastSeen   time.Time
	DaysActive int
	TripCount  int
	TopRoutes  []string // most frequently assigned routes (up to 3)
}

// AllVehicleStats returns operational stats for every known vehicle.
func (r *Repo) AllVehicleStats(ctx context.Context) ([]VehicleStats, error) {
	rows, err := r.db.Query(ctx, `
		SELECT v.vehicle_id, v.first_seen, v.last_seen,
			COUNT(DISTINCT va.date) AS days_active,
			COUNT(*) AS trip_count
		FROM transit.vehicle v
		LEFT JOIN transit.vehicle_assignment va ON va.vehicle_id = v.vehicle_id
		GROUP BY v.vehicle_id, v.first_seen, v.last_seen
		ORDER BY v.vehicle_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []VehicleStats
	for rows.Next() {
		var s VehicleStats
		if err := rows.Scan(&s.VehicleID, &s.FirstSeen, &s.LastSeen, &s.DaysActive, &s.TripCount); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch top routes per vehicle
	routeRows, err := r.db.Query(ctx, `
		SELECT vehicle_id, route_id, COUNT(*) AS cnt
		FROM transit.vehicle_assignment
		GROUP BY vehicle_id, route_id
		ORDER BY vehicle_id, cnt DESC`)
	if err != nil {
		return stats, nil // degrade gracefully
	}
	defer routeRows.Close()

	routeMap := make(map[string][]string)
	for routeRows.Next() {
		var vid, rid string
		var cnt int
		if err := routeRows.Scan(&vid, &rid, &cnt); err != nil {
			continue
		}
		if len(routeMap[vid]) < 3 {
			routeMap[vid] = append(routeMap[vid], rid)
		}
	}
	for i := range stats {
		stats[i].TopRoutes = routeMap[stats[i].VehicleID]
	}

	return stats, nil
}

// --- Data Range ---

// --- Stop Analytics ---

// StopAnalyticsRow is a per-stop summary from GPS-based visit data.
type StopAnalyticsRow struct {
	StopID        string   `json:"stop_id"`
	StopName      string   `json:"stop_name"`
	Lat           float64  `json:"lat"`
	Lon           float64  `json:"lon"`
	LastServiced  *string  `json:"last_serviced"` // ISO timestamp or nil
	RoutesServing int      `json:"routes_serving"`
	RouteIDs      []string `json:"route_ids"` // which routes serve this stop
	TotalVisits   int      `json:"total_visits"`
	AvgHeadwayMin *float64 `json:"avg_headway_min"` // nil if insufficient data
}

// StopAnalytics returns per-stop service data from the transit.stop_visit table.
func (r *Repo) StopAnalytics(ctx context.Context, days int) ([]StopAnalyticsRow, error) {
	rows, err := r.db.Query(ctx, `
		WITH visit_headways AS (
			SELECT
				sv.stop_id,
				sv.route_id,
				sv.observed_at,
				LAG(sv.observed_at) OVER (
					PARTITION BY sv.stop_id, sv.route_id, (sv.observed_at AT TIME ZONE 'America/Thunder_Bay')::date
					ORDER BY sv.observed_at
				) AS prev_observed
			FROM transit.stop_visit sv
			WHERE sv.observed_at >= CURRENT_DATE - $1::int
			  AND EXISTS (
				SELECT 1
				FROM transit.route_pattern rp
				JOIN transit.route_pattern_stop rps USING (pattern_id)
				WHERE rp.route_id = sv.route_id
				  AND rps.stop_id = sv.stop_id
				  AND rps.is_timepoint = true
			  )
		),
		headways AS (
			SELECT stop_id,
				EXTRACT(EPOCH FROM observed_at - prev_observed) / 60.0 AS headway_min
			FROM visit_headways
			WHERE prev_observed IS NOT NULL
		)
		SELECT
			s.stop_id,
			COALESCE(s.name, s.stop_id) AS stop_name,
			s.latitude,
			s.longitude,
			(SELECT MAX(sv2.observed_at)::TEXT FROM transit.stop_visit sv2 WHERE sv2.stop_id = s.stop_id) AS last_serviced,
			COALESCE(rc.route_count, 0)::INT AS routes_serving,
			COALESCE(rc.route_list, ARRAY[]::TEXT[]) AS route_ids,
			COALESCE(vc.visit_count, 0)::INT AS total_visits,
			h.avg_headway
		FROM transit.stop s
		LEFT JOIN (
			SELECT stop_id,
				COUNT(DISTINCT route_id) AS route_count,
				ARRAY_AGG(DISTINCT route_id ORDER BY route_id) AS route_list
			FROM transit.stop_visit WHERE observed_at >= CURRENT_DATE - $1::int
			GROUP BY stop_id
		) rc ON rc.stop_id = s.stop_id
		LEFT JOIN (
			SELECT stop_id, COUNT(*) AS visit_count
			FROM transit.stop_visit WHERE observed_at >= CURRENT_DATE - $1::int
			GROUP BY stop_id
		) vc ON vc.stop_id = s.stop_id
		LEFT JOIN (
			SELECT stop_id, AVG(headway_min) AS avg_headway
			FROM headways
			WHERE headway_min >= 1 AND headway_min <= 120
			GROUP BY stop_id
		) h ON h.stop_id = s.stop_id
		WHERE s.latitude IS NOT NULL AND s.longitude IS NOT NULL
		  AND COALESCE(vc.visit_count, 0) > 0
		ORDER BY COALESCE(vc.visit_count, 0) DESC
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []StopAnalyticsRow
	for rows.Next() {
		var r StopAnalyticsRow
		if err := rows.Scan(&r.StopID, &r.StopName, &r.Lat, &r.Lon,
			&r.LastServiced, &r.RoutesServing, &r.RouteIDs, &r.TotalVisits, &r.AvgHeadwayMin); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// StopLastServed returns the most recent visit time for each stop today.
func (r *Repo) StopLastServed(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.db.Query(ctx, `
		SELECT stop_id, MAX(observed_at) AS last_served
		FROM transit.stop_visit
		WHERE observed_at >= $1::date AND observed_at < ($1::date + 1)
		GROUP BY stop_id`, ServiceDate())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]time.Time, 500)
	for rows.Next() {
		var stopID string
		var ts time.Time
		if err := rows.Scan(&stopID, &ts); err != nil {
			return nil, err
		}
		result[stopID] = ts
	}
	return result, rows.Err()
}
