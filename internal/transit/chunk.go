package transit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/transit/chunk"
	"thundercitizen/internal/transit/recipes"
)

// This file is the DB-touching shell around the chunk math at
// internal/transit/chunk and the per-metric recipes at
// internal/transit/recipes. BuildChunk is a thin orchestrator that calls
// each recipe in turn and assembles a chunk.Chunk; the actual SQL for
// each metric concept lives next to its doc-comment in the recipes
// package, where it can be audited as a small standalone artifact.

// BuildChunk assembles one (route_id, date, band) chunk by running the
// five recipe queries and combining their results. Each recipe is a
// small focused SELECT against one source table — see
// internal/transit/recipes for the per-metric definitions.
//
// Five round trips per chunk. At civic data volume (~60 chunks per
// nightly fetcher run), that's ~300 ms total — a price we happily pay
// for the auditability that comes from each metric having its own
// readable SQL file.
func BuildChunk(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, b Band) (*chunk.Chunk, error) {
	wrap := func(stage string, err error) error {
		return fmt.Errorf("BuildChunk %s/%s/%s %s: %w", routeID, date.Format("2006-01-02"), b.Name, stage, err)
	}

	serviceKind, err := recipes.ServiceKind(ctx, db, routeID, date)
	if err != nil {
		return nil, wrap("service_kind", err)
	}

	otp, err := recipes.OTP(ctx, db, routeID, date, b.Name)
	if err != nil {
		return nil, wrap("otp", err)
	}

	cancel, err := recipes.Cancel(ctx, db, routeID, date, b.StartTime(), b.EndTime())
	if err != nil {
		return nil, wrap("cancel", err)
	}

	baseline, err := recipes.Baseline(ctx, db, routeID, b.Name, serviceKind)
	if err != nil {
		return nil, wrap("baseline", err)
	}

	headway, err := recipes.Headway(ctx, db, routeID, date, b.StartHour, b.EndHour)
	if err != nil {
		return nil, wrap("headway", err)
	}

	// Empty-chunk short-circuit: if every recipe returned zeros AND we
	// have no service_kind, the route has no presence on this date at
	// all. Returning (nil, nil) lets BuildChunksForDate skip empty rows.
	if serviceKind == "" &&
		otp.Trips == 0 && otp.OnTime == 0 &&
		cancel.Cancelled == 0 && cancel.NoNotice == 0 &&
		baseline.Scheduled == 0 && baseline.HeadwaySec == 0 &&
		headway.Count == 0 && headway.SumSec == 0 && headway.SumSecSq == 0 {
		return nil, nil
	}

	return &chunk.Chunk{
		RouteID:         routeID,
		Date:            date,
		Band:            b.Name,
		ServiceKind:     serviceKind,
		TripCount:       otp.Trips,
		OnTimeCount:     otp.OnTime,
		ScheduledCount:  baseline.Scheduled,
		CancelledCount:  cancel.Cancelled,
		NoNoticeCount:   cancel.NoNotice,
		HeadwayCount:    headway.Count,
		HeadwaySumSec:   headway.SumSec,
		HeadwaySumSecSq: headway.SumSecSq,
		SchedHeadwaySec: baseline.HeadwaySec,
		BuiltAt:         time.Now(),
	}, nil
}

// upsertChunk writes one chunk row, replacing any existing row for the
// same (route_id, date, band) triple.
func upsertChunk(ctx context.Context, db *pgxpool.Pool, ck *chunk.Chunk) error {
	_, err := db.Exec(ctx, `
		INSERT INTO transit.route_band_chunk (
			route_id, date, band, service_kind,
			trip_count, on_time_count,
			scheduled_count, cancelled_count, no_notice_count,
			headway_count, headway_sum_sec, headway_sum_sec_sq, sched_headway_sec,
			built_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
		ON CONFLICT (route_id, date, band) DO UPDATE SET
			service_kind = EXCLUDED.service_kind,
			trip_count = EXCLUDED.trip_count,
			on_time_count = EXCLUDED.on_time_count,
			scheduled_count = EXCLUDED.scheduled_count,
			cancelled_count = EXCLUDED.cancelled_count,
			no_notice_count = EXCLUDED.no_notice_count,
			headway_count = EXCLUDED.headway_count,
			headway_sum_sec = EXCLUDED.headway_sum_sec,
			headway_sum_sec_sq = EXCLUDED.headway_sum_sec_sq,
			sched_headway_sec = EXCLUDED.sched_headway_sec,
			built_at = now()
	`,
		ck.RouteID, ck.Date, ck.Band, ck.ServiceKind,
		ck.TripCount, ck.OnTimeCount,
		ck.ScheduledCount, ck.CancelledCount, ck.NoNoticeCount,
		ck.HeadwayCount, ck.HeadwaySumSec, ck.HeadwaySumSecSq, ck.SchedHeadwaySec,
	)
	return err
}

// BuildChunksForDate iterates every active route × every band and
// upserts a chunk row for each. Idempotent — running it twice produces
// the same rows. Routes with no observations on the date are skipped
// (BuildChunk returns nil for them).
func BuildChunksForDate(ctx context.Context, db *pgxpool.Pool, date time.Time) (int, error) {
	repo := NewRepo(db)
	// Use a 7-day window centered on the target date so AllRouteMeta
	// returns every route that ran on that date.
	from := date.AddDate(0, 0, -3)
	to := date.AddDate(0, 0, 3)
	routes, err := repo.AllRouteMeta(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("list routes: %w", err)
	}
	n := 0
	for _, rm := range routes {
		for _, b := range Bands {
			ck, err := BuildChunk(ctx, db, rm.RouteID, date, b)
			if err != nil {
				return n, err
			}
			if ck == nil {
				continue
			}
			if err := upsertChunk(ctx, db, ck); err != nil {
				return n, fmt.Errorf("upsert %s/%s/%s: %w", rm.RouteID, date.Format("2006-01-02"), b.Name, err)
			}
			n++
		}
	}
	return n, nil
}

// TimepointVisit is one (trip, timepoint stop) row in a ChunkDetail —
// scheduled time from transit.scheduled_stop and (optionally) observed
// time from transit.stop_visit. ObservedAt is nil when the bus never
// reached the stop in the GPS feed (cancelled trip, GPS gap, or the
// visit hasn't been recorded yet).
//
// ObservedAt is the GPS-line-segment-interpolated arrival time written
// by the recorder in vehicle_tracker.go's processPositions: when a
// position ping lands within 50 m of a stop's segment, the recorder
// linearly interpolates along the segment to estimate the moment the
// bus was nearest the stop. See CLAUDE.md "Stop visit detection" for
// the full algorithm.
type TimepointVisit struct {
	TripID             string     `json:"trip_id"`
	StopID             string     `json:"stop_id"`
	StopName           string     `json:"stop_name"`
	StopSequence       int        `json:"stop_sequence"`
	ScheduledDeparture string     `json:"scheduled_departure"` // "HH:MM:SS" GTFS time string (may exceed 24:00)
	ObservedAt         *time.Time `json:"observed_at,omitempty"`
	DelaySec           *int       `json:"delay_sec,omitempty"`
}

// ChunkDetail is the per-trip per-timepoint drill-down for one
// (route_id, date, band) chunk. Not stored — computed on demand from
// transit.trip_catalog + transit.scheduled_stop + transit.stop_visit.
// Intended as the data source for a "show me what actually happened on
// route 3 last Tuesday morning" UI surface.
type ChunkDetail struct {
	chunk.Chunk
	// Trips keyed by trip_id, each value is the trip's ordered list of
	// timepoint visits (scheduled + observed). Order within each value
	// matches stop_sequence.
	Trips map[string][]TimepointVisit `json:"trips"`
}

// LoadChunkDetail returns the per-trip per-timepoint drill-down for one
// chunk. The Chunk field is filled from a fresh BuildChunk call so the
// detail is internally consistent with its own counts (no risk of stale
// rollup vs fresh detail). Returns nil if there's no evidence of the
// chunk existing.
func LoadChunkDetail(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, b Band) (*ChunkDetail, error) {
	ck, err := BuildChunk(ctx, db, routeID, date, b)
	if err != nil {
		return nil, err
	}
	if ck == nil {
		return nil, nil
	}
	out := &ChunkDetail{
		Chunk: *ck,
		Trips: map[string][]TimepointVisit{},
	}

	// One query — every (trip, timepoint stop) row for trips in this
	// (route, date, band), with the observed visit LEFT JOIN'd in. Trips
	// without any observed visits still appear (e.g. cancelled trips).
	rows, err := db.Query(ctx, `
		SELECT
			tc.trip_id,
			ss.stop_id,
			COALESCE(s.name, '') AS stop_name,
			ss.stop_sequence,
			COALESCE(ss.scheduled_departure, ss.scheduled_arrival, '') AS sched,
			sv.observed_at,
			sd.arrival_delay
		FROM transit.trip_catalog tc
		JOIN transit.scheduled_stop ss
			ON ss.trip_id = tc.trip_id
			AND ss.is_timepoint = true
		LEFT JOIN transit.stop s ON s.stop_id = ss.stop_id
		LEFT JOIN transit.stop_visit sv
			ON sv.trip_id = tc.trip_id
			AND sv.stop_id = ss.stop_id
			AND sv.observed_at >= $2::date
			AND sv.observed_at < ($2::date + 1)
		LEFT JOIN transit.stop_delay sd
			ON sd.date = $2::date
			AND sd.trip_id = tc.trip_id
			AND sd.stop_id = ss.stop_id
		WHERE tc.route_id = $1
		  AND tc.band = $3
		  AND EXISTS (
			SELECT 1 FROM transit.service_calendar sc
			WHERE sc.service_id = tc.service_id
			  AND sc.date = $2::date
		  )
		ORDER BY tc.trip_id, ss.stop_sequence
	`, routeID, date, b.Name)
	if err != nil {
		return nil, fmt.Errorf("LoadChunkDetail %s/%s/%s: %w", routeID, date.Format("2006-01-02"), b.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var v TimepointVisit
		var observedAt *time.Time
		var delay *int32
		if err := rows.Scan(&v.TripID, &v.StopID, &v.StopName, &v.StopSequence, &v.ScheduledDeparture, &observedAt, &delay); err != nil {
			return nil, err
		}
		v.ObservedAt = observedAt
		if delay != nil {
			d := int(*delay)
			v.DelaySec = &d
		}
		out.Trips[v.TripID] = append(out.Trips[v.TripID], v)
	}
	return out, rows.Err()
}
