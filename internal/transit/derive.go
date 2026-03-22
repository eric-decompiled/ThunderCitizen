package transit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// derive.go owns the Tier 1 → Tier 2 transformation. After the loader has
// bulk-staged the GTFS bundle into the gtfs.* schema, DeriveTier2 builds the
// stable application entities (transit.route, transit.stop,
// transit.route_pattern, transit.route_pattern_stop, transit.trip_catalog,
// transit.service_calendar, transit.route_baseline, transit.scheduled_stop)
// from the staging tables.
//
// The whole derive runs inside a single transaction. Readers see the previous
// Tier 2 state until COMMIT, then the new state. If any derive step fails,
// the transaction rolls back and Tier 2 stays at the previous good state.
// This is the resilience contract that makes the rest of the application
// safe against GTFS feed disruptions.

// DeriveTier2 builds every Tier 2 entity from the gtfs.* staging tables.
// Must be called after loadStagingGTFS has populated gtfs.*.
func DeriveTier2(ctx context.Context, db *pgxpool.Pool) error {
	gtfsLog.Info("deriving Tier 2 entities")

	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// TRUNCATE Tier 2 in dependency order (children first). CASCADE handles
	// the dependent FK references in one shot.
	if _, err := tx.Exec(ctx, `
		TRUNCATE
			transit.scheduled_stop,
			transit.trip_catalog,
			transit.route_baseline,
			transit.service_calendar,
			transit.route_pattern_stop,
			transit.route_pattern,
			transit.stop,
			transit.route
		CASCADE
	`); err != nil {
		return fmt.Errorf("truncate transit.*: %w", err)
	}

	steps := []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
	}{
		{"route", deriveRoute},
		{"stop", deriveStop},
		{"route_pattern", deriveRoutePatterns},
		{"service_calendar", deriveServiceCalendar},
		{"trip_catalog", deriveTripCatalog},
		{"route_baseline", deriveRouteBaseline},
		{"scheduled_stop", deriveScheduledStop},
	}

	for _, s := range steps {
		if err := s.fn(ctx, tx); err != nil {
			return fmt.Errorf("derive %s: %w", s.name, err)
		}
		gtfsLog.Info("derived", "entity", s.name)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit derive: %w", err)
	}
	return nil
}

// deriveRoute builds transit.route from gtfs.routes, overlaying the
// curated long names from routeLongNames (route_long_names.go). Thunder
// Bay's GTFS feed leaves long_name empty for every route, so without
// the overlay every card prints its id twice. The overlay is keyed by
// route_id and applied via a JOIN against an UNNEST'd VALUES list, so
// updating the names is a one-line Go edit — no SQL or migration.
//
// The display_name falls through long_name first, then the most-common
// headsign prefix from gtfs.trips (the "to X" suffix is stripped), then
// the route_id. With long_name populated by the overlay, display_name
// always lands on the curated name in practice.
func deriveRoute(ctx context.Context, tx pgx.Tx) error {
	// Build parallel arrays for the overlay so we can pass them as one
	// UNNEST and join in pure SQL. Order doesn't matter — the JOIN keys
	// on route_id.
	ids := make([]string, 0, len(routeLongNames))
	names := make([]string, 0, len(routeLongNames))
	for id, name := range routeLongNames {
		ids = append(ids, id)
		names = append(names, name)
	}

	_, err := tx.Exec(ctx, `
		WITH long_name_overlay AS (
			SELECT route_id, long_name
			FROM UNNEST($1::text[], $2::text[]) AS t(route_id, long_name)
		),
		headsign_display AS (
			SELECT DISTINCT ON (t.route_id)
				t.route_id,
				INITCAP(
					CASE WHEN POSITION(' to ' IN t.headsign) > 0
					     THEN SUBSTRING(t.headsign FROM 1 FOR POSITION(' to ' IN t.headsign) - 1)
					     ELSE t.headsign
					END
				) AS display_name
			FROM gtfs.trips t
			WHERE t.headsign IS NOT NULL AND t.headsign != ''
			ORDER BY t.route_id, COUNT(*) OVER (PARTITION BY t.route_id, t.headsign) DESC
		)
		INSERT INTO transit.route (
			route_id, short_name, long_name, display_name, route_type, color, text_color, sort_order
		)
		SELECT
			r.route_id,
			COALESCE(r.short_name, ''),
			COALESCE(NULLIF(lo.long_name, ''), NULLIF(r.long_name, ''), '') AS long_name,
			COALESCE(NULLIF(lo.long_name, ''), NULLIF(r.long_name, ''), hd.display_name, r.route_id) AS display_name,
			r.route_type,
			COALESCE(r.color, ''),
			COALESCE(r.text_color, ''),
			0
		FROM gtfs.routes r
		LEFT JOIN long_name_overlay lo ON lo.route_id = r.route_id
		LEFT JOIN headsign_display hd ON hd.route_id = r.route_id
	`, ids, names)
	return err
}

// deriveStop builds transit.stop from gtfs.stops. Stops with missing
// coordinates are skipped — they can't be plotted and the rest of the app
// requires lat/lon. The geog column is filled by the trg_transit_stop_geog
// trigger on insert.
func deriveStop(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO transit.stop (
			stop_id, name, code, latitude, longitude, parent_station,
			wheelchair, is_terminal, is_transfer
		)
		SELECT
			s.stop_id,
			COALESCE(NULLIF(s.stop_name, ''), s.stop_id),
			COALESCE(s.stop_code, ''),
			s.latitude,
			s.longitude,
			COALESCE(s.parent_station, ''),
			COALESCE(s.wheelchair = 1, false),
			false,
			EXISTS (SELECT 1 FROM gtfs.transfers tr WHERE tr.from_stop_id = s.stop_id)
		FROM gtfs.stops s
		WHERE s.latitude IS NOT NULL AND s.longitude IS NOT NULL
	`)
	return err
}

// deriveRoutePatterns rebuilds transit.route_pattern and
// transit.route_pattern_stop from gtfs.trips + gtfs.stop_times. For each
// (route_id, headsign, direction_id), it picks the most common stop sequence
// across the trips that share that key — that's the "canonical" pattern. The
// is_timepoint flag travels with each stop in the pattern.
//
// Pattern IDs are deterministic: route_id + direction + slugged headsign.
// They survive across reloads as long as the route's structure is unchanged.
func deriveRoutePatterns(ctx context.Context, tx pgx.Tx) error {
	// Stage canonical patterns into a temp table so route_pattern and
	// route_pattern_stop can both read from it without re-computing.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE _canonical_patterns ON COMMIT DROP AS
		WITH trip_patterns AS (
			SELECT
				t.route_id,
				COALESCE(t.headsign, '') AS headsign,
				COALESCE(t.direction_id, 0) AS direction_id,
				t.trip_id,
				ARRAY_AGG(st.stop_id ORDER BY st.stop_sequence) AS stop_ids,
				ARRAY_AGG(st.timepoint ORDER BY st.stop_sequence) AS timepoints
			FROM gtfs.trips t
			JOIN gtfs.stop_times st ON st.trip_id = t.trip_id
			GROUP BY t.route_id, t.headsign, t.direction_id, t.trip_id
		),
		ranked AS (
			SELECT
				route_id, headsign, direction_id,
				stop_ids, timepoints,
				COUNT(*) AS trip_count,
				ROW_NUMBER() OVER (
					PARTITION BY route_id, headsign, direction_id
					ORDER BY COUNT(*) DESC
				) AS rn
			FROM trip_patterns
			GROUP BY route_id, headsign, direction_id, stop_ids, timepoints
		)
		SELECT
			route_id || '-' || direction_id || '-' ||
				REGEXP_REPLACE(LOWER(headsign), '[^a-z0-9]+', '_', 'g') AS pattern_id,
			route_id, headsign, direction_id, stop_ids, timepoints,
			array_length(stop_ids, 1) AS stop_count,
			(SELECT COUNT(*) FROM unnest(timepoints) tp WHERE tp = true) AS timepoint_count
		FROM ranked
		WHERE rn = 1
	`); err != nil {
		return fmt.Errorf("stage canonical_patterns: %w", err)
	}

	// Insert the patterns themselves.
	if _, err := tx.Exec(ctx, `
		INSERT INTO transit.route_pattern (
			pattern_id, route_id, headsign, direction_id, stop_count, timepoint_count
		)
		SELECT pattern_id, route_id, headsign, direction_id, stop_count, timepoint_count
		FROM _canonical_patterns
	`); err != nil {
		return fmt.Errorf("insert route_pattern: %w", err)
	}

	// Then the per-pattern stop sequences. unnest WITH ORDINALITY gives us a
	// 1-indexed position which becomes the sequence column.
	// We filter to stops that actually exist in transit.stop so the FK holds.
	if _, err := tx.Exec(ctx, `
		INSERT INTO transit.route_pattern_stop (pattern_id, sequence, stop_id, is_timepoint)
		SELECT
			c.pattern_id,
			u.ord::int AS sequence,
			u.stop_id,
			u.is_tp
		FROM _canonical_patterns c
		CROSS JOIN LATERAL unnest(c.stop_ids, c.timepoints)
			WITH ORDINALITY AS u(stop_id, is_tp, ord)
		WHERE EXISTS (SELECT 1 FROM transit.stop s WHERE s.stop_id = u.stop_id)
	`); err != nil {
		return fmt.Errorf("insert route_pattern_stop: %w", err)
	}

	return nil
}

// deriveServiceCalendar flattens gtfs.calendar + gtfs.calendar_dates into a
// simple (service_id, date, service_kind) catalog. Replaces the
// transit_calendar_dates exception_type semantics with a single yes/no view
// of "does this service run on this date".
//
// service_kind is derived from the day-of-week distribution of dates: a
// service that runs mostly on Saturdays is 'saturday', mostly Sundays is
// 'sunday', otherwise 'weekday'. Thunder Bay's GTFS uses calendar_dates only
// (no calendar.txt), so the dow heuristic is the right call.
func deriveServiceCalendar(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		WITH calendar_pattern AS (
			-- Expand gtfs.calendar (if present) into per-date rows
			SELECT
				c.service_id,
				d::date AS date
			FROM gtfs.calendar c
			CROSS JOIN LATERAL generate_series(c.start_date, c.end_date, '1 day'::interval) d
			WHERE
				(c.monday    AND EXTRACT(DOW FROM d)::int = 1) OR
				(c.tuesday   AND EXTRACT(DOW FROM d)::int = 2) OR
				(c.wednesday AND EXTRACT(DOW FROM d)::int = 3) OR
				(c.thursday  AND EXTRACT(DOW FROM d)::int = 4) OR
				(c.friday    AND EXTRACT(DOW FROM d)::int = 5) OR
				(c.saturday  AND EXTRACT(DOW FROM d)::int = 6) OR
				(c.sunday    AND EXTRACT(DOW FROM d)::int = 0)
		),
		calendar_added AS (
			SELECT service_id, date FROM gtfs.calendar_dates WHERE exception_type = 1
		),
		calendar_removed AS (
			SELECT service_id, date FROM gtfs.calendar_dates WHERE exception_type = 2
		),
		combined AS (
			SELECT service_id, date FROM calendar_pattern
			UNION
			SELECT service_id, date FROM calendar_added
			EXCEPT
			SELECT service_id, date FROM calendar_removed
		),
		dow_counts AS (
			SELECT
				service_id,
				COUNT(*) FILTER (WHERE EXTRACT(DOW FROM date)::int BETWEEN 1 AND 5) AS weekday_n,
				COUNT(*) FILTER (WHERE EXTRACT(DOW FROM date)::int = 6) AS saturday_n,
				COUNT(*) FILTER (WHERE EXTRACT(DOW FROM date)::int = 0) AS sunday_n
			FROM combined
			GROUP BY service_id
		),
		service_kinds AS (
			SELECT
				service_id,
				CASE
					WHEN saturday_n >= weekday_n AND saturday_n >= sunday_n AND saturday_n > 0 THEN 'saturday'
					WHEN sunday_n   >= weekday_n AND sunday_n   >= saturday_n AND sunday_n > 0 THEN 'sunday'
					ELSE 'weekday'
				END AS service_kind
			FROM dow_counts
		)
		INSERT INTO transit.service_calendar (service_id, date, service_kind)
		SELECT c.service_id, c.date, sk.service_kind
		FROM combined c
		JOIN service_kinds sk ON sk.service_id = c.service_id
	`)
	return err
}

// deriveTripCatalog rebuilds transit.trip_catalog from gtfs.trips, joining
// each trip to its route_pattern and computing scheduled first/last times +
// band assignment from gtfs.stop_times. Each trip ends up with all the
// context the recorder needs to enrich a stop_delay row at write time.
func deriveTripCatalog(ctx context.Context, tx pgx.Tx) error {
	// Stage per-trip first/last stop times into a temp table so the main
	// insert is a clean join.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE _trip_endpoints ON COMMIT DROP AS
		WITH ranked AS (
			SELECT
				trip_id,
				departure_time,
				arrival_time,
				ROW_NUMBER() OVER (PARTITION BY trip_id ORDER BY stop_sequence) AS r_first,
				ROW_NUMBER() OVER (PARTITION BY trip_id ORDER BY stop_sequence DESC) AS r_last
			FROM gtfs.stop_times
		),
		first_stops AS (
			SELECT trip_id, COALESCE(departure_time, arrival_time, '00:00:00') AS first_dep
			FROM ranked WHERE r_first = 1
		),
		last_stops AS (
			SELECT trip_id, COALESCE(arrival_time, departure_time, '00:00:00') AS last_arr
			FROM ranked WHERE r_last = 1
		)
		SELECT f.trip_id, f.first_dep, l.last_arr
		FROM first_stops f
		JOIN last_stops l ON l.trip_id = f.trip_id
	`); err != nil {
		return fmt.Errorf("stage trip_endpoints: %w", err)
	}

	// service_id → service_kind from the (already-built) service_calendar.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE _service_kinds ON COMMIT DROP AS
		SELECT DISTINCT service_id, service_kind FROM transit.service_calendar
	`); err != nil {
		return fmt.Errorf("stage service_kinds: %w", err)
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO transit.trip_catalog (
			trip_id, route_id, pattern_id, service_id, service_kind,
			headsign, direction_id, block_id, band,
			scheduled_first_dep_time, scheduled_last_arr_time
		)
		SELECT
			t.trip_id,
			t.route_id,
			rp.pattern_id,
			t.service_id,
			COALESCE(sk.service_kind, 'weekday') AS service_kind,
			COALESCE(t.headsign, ''),
			COALESCE(t.direction_id, 0),
			COALESCE(t.block_id, ''),
			CASE
				WHEN te.first_dep >= '06:00:00' AND te.first_dep < '12:00:00' THEN 'morning'
				WHEN te.first_dep >= '12:00:00' AND te.first_dep < '18:00:00' THEN 'midday'
				ELSE 'evening'
			END AS band,
			te.first_dep,
			te.last_arr
		FROM gtfs.trips t
		JOIN _trip_endpoints te ON te.trip_id = t.trip_id
		JOIN transit.route_pattern rp
			ON rp.route_id = t.route_id
			AND rp.headsign = COALESCE(t.headsign, '')
			AND rp.direction_id = COALESCE(t.direction_id, 0)
		LEFT JOIN _service_kinds sk ON sk.service_id = t.service_id
	`)
	return err
}

// deriveRouteBaseline computes the per (route, service_kind, band) schedule
// baseline from gtfs.stop_times. This is what cancel rate uses as its
// "scheduled trip count" denominator and what EWT uses as its scheduled
// headway baseline. Refreshed at GTFS load — between loads, the baseline is
// frozen, so metrics never depend on the volatile staging tables at runtime.
func deriveRouteBaseline(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		WITH band_defs(band, start_t, end_t) AS (
			VALUES
				('morning'::text, '06:00:00'::text, '12:00:00'::text),
				('midday',         '12:00:00',      '18:00:00'),
				('evening',        '18:00:00',      '24:00:00')
		),
		-- Trip count per (route, service_kind, band): trips with first-stop
		-- departure in band, grouped by service_kind via trip_catalog.
		trip_counts AS (
			SELECT
				tc.route_id,
				tc.service_kind,
				tc.band,
				COUNT(*) AS trip_count_total,
				COUNT(DISTINCT tc.service_id) AS service_id_n
			FROM transit.trip_catalog tc
			GROUP BY tc.route_id, tc.service_kind, tc.band
		),
		-- Scheduled headway at timepoint stops: LAG over departure_time within
		-- (route, stop, service_id, band).
		sched_headways AS (
			SELECT
				t.route_id,
				skd.service_kind,
				b.band,
				EXTRACT(EPOCH FROM (
					st.departure_time::interval - LAG(st.departure_time::interval) OVER (
						PARTITION BY t.route_id, st.stop_id, t.service_id, b.band
						ORDER BY st.departure_time::interval
					)
				)) AS headway_sec
			FROM gtfs.stop_times st
			JOIN gtfs.trips t ON t.trip_id = st.trip_id
			LEFT JOIN (
				SELECT DISTINCT service_id, service_kind FROM transit.service_calendar
			) skd ON skd.service_id = t.service_id
			CROSS JOIN band_defs b
			WHERE st.timepoint = TRUE
			  AND st.departure_time >= b.start_t
			  AND st.departure_time <  b.end_t
		),
		mean_headways AS (
			SELECT
				route_id,
				COALESCE(service_kind, 'weekday') AS service_kind,
				band,
				AVG(headway_sec) AS mean_headway_sec
			FROM sched_headways
			WHERE headway_sec > 60 AND headway_sec < 7200
			GROUP BY route_id, COALESCE(service_kind, 'weekday'), band
		)
		INSERT INTO transit.route_baseline (
			route_id, service_kind, band, scheduled_trip_count, scheduled_headway_sec, sample_n
		)
		SELECT
			tc.route_id,
			tc.service_kind,
			tc.band,
			-- Per-day average: total trip count / number of distinct service_ids
			GREATEST(1, ROUND(tc.trip_count_total::numeric / NULLIF(tc.service_id_n, 0))::int) AS scheduled_trip_count,
			COALESCE(ROUND(mh.mean_headway_sec)::int, 0) AS scheduled_headway_sec,
			tc.service_id_n AS sample_n
		FROM trip_counts tc
		LEFT JOIN mean_headways mh
			ON mh.route_id = tc.route_id
			AND mh.service_kind = tc.service_kind
			AND mh.band = tc.band
		WHERE EXISTS (SELECT 1 FROM transit.route r WHERE r.route_id = tc.route_id)
	`)
	return err
}

// deriveScheduledStop builds a self-contained per-trip per-stop schedule
// table from gtfs.stop_times + gtfs.trips. Used by schedule-display queries
// (route timetable, today's running trips, route schedule with cancellations)
// so they can read entirely from transit.* without joining back through the
// gtfs.* staging schema.
//
// Functionally a snapshot of gtfs.stop_times with the trip's route/headsign/
// service_id/pattern_id denormalized onto each row.
func deriveScheduledStop(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO transit.scheduled_stop (
			trip_id, stop_sequence, stop_id, scheduled_arrival, scheduled_departure,
			is_timepoint, route_id, pattern_id, service_id, headsign
		)
		SELECT
			st.trip_id,
			st.stop_sequence,
			st.stop_id,
			st.arrival_time,
			st.departure_time,
			COALESCE(st.timepoint, false),
			tc.route_id,
			tc.pattern_id,
			tc.service_id,
			tc.headsign
		FROM gtfs.stop_times st
		JOIN transit.trip_catalog tc ON tc.trip_id = st.trip_id
		WHERE EXISTS (SELECT 1 FROM transit.stop s WHERE s.stop_id = st.stop_id)
	`)
	return err
}
