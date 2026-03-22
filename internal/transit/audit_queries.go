package transit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// auditRouteTimepointSchedule is the audit-page counterpart to
// RouteTimepointSchedule. It returns one TimepointSchedule per direction
// (headsign) on the route, but each direction's stops list is the UNION of
// every timepoint visited by any trip in that direction — not just the
// representative trip's stops. This gives the maintainer wider rows with
// more timepoints to compare obs/gps/Δ/dwell against.
//
// The trip rows themselves come from the same query shape as
// buildDirectionSchedule — each (trip, timepoint) row populates a cell at
// the stop's index in the combined stops list.
func auditRouteTimepointSchedule(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time) ([]TimepointSchedule, error) {
	dirRows, err := db.Query(ctx, `
		SELECT DISTINCT tc.headsign
		FROM transit.trip_catalog tc
		JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
		WHERE tc.route_id = $1 AND sc.date = $2
		ORDER BY tc.headsign
	`, routeID, date)
	if err != nil {
		return nil, err
	}
	defer dirRows.Close()

	var headsigns []string
	for dirRows.Next() {
		var h string
		if err := dirRows.Scan(&h); err != nil {
			return nil, err
		}
		headsigns = append(headsigns, h)
	}
	if err := dirRows.Err(); err != nil {
		return nil, err
	}

	var schedules []TimepointSchedule
	for _, h := range headsigns {
		sched, err := buildAuditDirectionSchedule(ctx, db, routeID, date, h)
		if err != nil {
			return nil, err
		}
		if sched != nil && len(sched.Trips) > 0 {
			schedules = append(schedules, *sched)
		}
	}

	// Same merging policy as RouteTimepointSchedule: fold small directions
	// (< 3 trips, typically one-off service variants) into the best-matching
	// main direction so the maintainer sees a small number of grids instead
	// of one grid per headsign variant.
	const minTrips = 3
	var main, small []TimepointSchedule
	for _, s := range schedules {
		if len(s.Trips) >= minTrips {
			main = append(main, s)
		} else {
			small = append(small, s)
		}
	}
	for _, s := range small {
		bestIdx := findBestMergeTarget(main, s)
		if bestIdx < 0 {
			main = append(main, s)
			continue
		}
		main[bestIdx] = mergeDirection(main[bestIdx], s)
	}
	return main, nil
}

// buildAuditDirectionSchedule mirrors buildDirectionSchedule but pulls the
// UNION of every scheduled stop (not just timepoints) across every trip in
// the direction. Widest possible grid — the maintainer wants to see every
// stop the bus passes, not just the designated timing stops.
func buildAuditDirectionSchedule(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, headsign string) (*TimepointSchedule, error) {
	stopRows, err := db.Query(ctx, `
		WITH dir_trips AS (
			SELECT tc.trip_id
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE tc.route_id = $1 AND sc.date = $2 AND tc.headsign = $3
		)
		SELECT ss.stop_id,
		       COALESCE(s.name, ss.stop_id) AS stop_name,
		       MIN(ss.stop_sequence) AS first_seq
		FROM transit.scheduled_stop ss
		JOIN dir_trips USING (trip_id)
		LEFT JOIN transit.stop s ON s.stop_id = ss.stop_id
		GROUP BY ss.stop_id, s.name
		ORDER BY first_seq, ss.stop_id
	`, routeID, date, headsign)
	if err != nil {
		return nil, err
	}
	defer stopRows.Close()

	var stops []TimepointStop
	stopIndex := map[string]int{}
	for stopRows.Next() {
		var s TimepointStop
		if err := stopRows.Scan(&s.StopID, &s.StopName, &s.Sequence); err != nil {
			return nil, err
		}
		stopIndex[s.StopID] = len(stops)
		stops = append(stops, s)
	}
	if err := stopRows.Err(); err != nil {
		return nil, err
	}
	if len(stops) == 0 {
		return nil, nil
	}

	rows, err := db.Query(ctx, `
		SELECT
			tc.trip_id, tc.headsign,
			ss.stop_id,
			COALESCE(ss.scheduled_departure, ss.scheduled_arrival) AS sched_time,
			a.arrival_delay, a.departure_delay,
			CASE WHEN cn.trip_id IS NOT NULL THEN TRUE ELSE FALSE END AS canceled
		FROM transit.trip_catalog tc
		JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
		JOIN transit.scheduled_stop ss
			ON ss.trip_id = tc.trip_id
		LEFT JOIN transit.stop_delay a
			ON a.trip_id = tc.trip_id AND a.stop_id = ss.stop_id AND a.date = $2
		LEFT JOIN transit.cancellation cn
			ON cn.trip_id = tc.trip_id AND cn.feed_timestamp::DATE = $2
		WHERE tc.route_id = $1 AND tc.headsign = $3 AND sc.date = $2
		ORDER BY sched_time, tc.trip_id, ss.stop_sequence
	`, routeID, date, headsign)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tripMap := map[string]*TimepointTrip{}
	var tripOrder []string
	for rows.Next() {
		var tripID, hs, stopID, schedTime string
		var delaySec, depDelay *int
		var canceled bool
		if err := rows.Scan(&tripID, &hs, &stopID, &schedTime, &delaySec, &depDelay, &canceled); err != nil {
			return nil, err
		}

		trip, ok := tripMap[tripID]
		if !ok {
			trip = &TimepointTrip{
				TripID:   tripID,
				Headsign: hs,
				Canceled: canceled,
				Stops:    make([]TripTimepoint, len(stops)),
			}
			tripMap[tripID] = trip
			tripOrder = append(tripOrder, tripID)
		}

		idx, ok := stopIndex[stopID]
		if !ok {
			continue
		}
		trip.Stops[idx] = TripTimepoint{
			ScheduledTime:  trimTime(schedTime),
			DelaySec:       delaySec,
			DepartureDelay: depDelay,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trips := make([]TimepointTrip, len(tripOrder))
	for i, id := range tripOrder {
		trips[i] = *tripMap[id]
	}

	return &TimepointSchedule{Headsign: headsign, Stops: stops, Trips: trips}, nil
}

// gpsVisit is one row of GPS-derived stop-visit data for the audit view.
// DelaySec is the midpoint-based GPS delay (entered_at + exited_at)/2 minus
// scheduled time, or entered_at alone while a visit is still in progress.
// InsidePolls is the count of VehiclePosition polls that landed inside the
// 50m radius during the visit; the audit page's drive-by classifier treats
// InsidePolls <= 1 as a drive-by rather than a service stop.
type gpsVisit struct {
	TripID      string
	StopID      string
	DelaySec    int
	EnteredAt   time.Time
	ExitedAt    *time.Time
	InsidePolls *int
}

// loadRouteGpsVisits returns GPS-derived delays for every timepoint visit on
// a route for a given service date, keyed by trip_id then stop_id.
//
// The GPS signal comes from transit.stop_visit, which the vehicle_tracker
// writes as entry/exit events (see internal/transit/vehicle_tracker.go). The
// delay used here is against the MIDPOINT of entered_at and exited_at when
// both are available (more accurate than first-touch, which would bias the
// GPS signal ~5-15s early). For in-progress visits we fall back to
// entered_at alone.
//
// This is a completely independent pipeline from the trip-updates feed that
// populates transit.stop_delay. On the audit page the two appear side by
// side as "obs" vs "gps" for cross-examination.
//
// Note: observed_at on the table still carries the first-touch entry
// timestamp for backward compatibility with headway/chunk/repo readers. The
// midpoint is computed here at read time, never persisted.
func loadRouteGpsVisits(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time) (map[string]map[string]gpsVisit, error) {
	// Day window in Thunder Bay local time. scheduled_stop has no date column,
	// so we disambiguate multi-day trip_ids by the entered_at range.
	rows, err := db.Query(ctx, `
		SELECT
			sv.trip_id,
			sv.stop_id,
			EXTRACT(EPOCH FROM (
				COALESCE(
					sv.entered_at + (sv.exited_at - sv.entered_at) / 2,
					sv.entered_at
				) - (
					($2::date + COALESCE(ss.scheduled_departure, ss.scheduled_arrival)::interval)
					AT TIME ZONE 'America/Thunder_Bay'
				)
			))::int AS delay_sec,
			sv.entered_at,
			sv.exited_at,
			sv.inside_polls
		FROM transit.stop_visit sv
		JOIN transit.scheduled_stop ss
			ON ss.trip_id = sv.trip_id AND ss.stop_id = sv.stop_id
		WHERE ss.route_id = $1
			AND sv.entered_at >= ($2::date) AT TIME ZONE 'America/Thunder_Bay'
			AND sv.entered_at <  ($2::date + INTERVAL '1 day') AT TIME ZONE 'America/Thunder_Bay'
	`, routeID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]map[string]gpsVisit)
	for rows.Next() {
		var v gpsVisit
		if err := rows.Scan(&v.TripID, &v.StopID, &v.DelaySec, &v.EnteredAt, &v.ExitedAt, &v.InsidePolls); err != nil {
			return nil, err
		}
		trip, ok := out[v.TripID]
		if !ok {
			trip = make(map[string]gpsVisit)
			out[v.TripID] = trip
		}
		trip[v.StopID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
