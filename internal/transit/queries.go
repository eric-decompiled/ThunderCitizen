package transit

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trimTime truncates "HH:MM:SS" to "HH:MM".
func trimTime(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}

// DelayPercentileBucket is a time bucket with P50/P90/P99/P99.9 delay values.
type DelayPercentileBucket struct {
	BucketTime time.Time `json:"time"`
	P50        float64   `json:"p50"`
	P90        float64   `json:"p90"`
	P99        float64   `json:"p99"`
	P999       float64   `json:"p999"`
	Count      int       `json:"count"`
}

// DayPercentiles returns delay percentile curves for the last 24 hours,
// bucketed into 30-minute intervals from transit.stop_delay.
func DayPercentiles(ctx context.Context, db *pgxpool.Pool) ([]DelayPercentileBucket, error) {
	return NewRepo(db).DayPercentiles(ctx)
}

// DaySnapshots returns 5-min snapshots for the last 24 hours,
// derived from raw event tables.
func DaySnapshots(ctx context.Context, db *pgxpool.Pool) ([]TransitSnapshot, error) {
	return NewRepo(db).DaySnapshots(ctx)
}

// WeekSummary returns daily aggregates for the last 7 days, derived from raw events.
func WeekSummary(ctx context.Context, db *pgxpool.Pool) ([]DaySummary, error) {
	return NewRepo(db).WeekSummary(ctx)
}

// NoServiceRoutes returns route IDs that have zero scheduled trips today.
func NoServiceRoutes(ctx context.Context, db *pgxpool.Pool, date time.Time) ([]string, error) {
	rows, err := db.Query(ctx, `
		WITH active_routes AS (
			SELECT DISTINCT tc.route_id
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE sc.date = $1
		)
		SELECT r.route_id FROM transit.route r
		WHERE r.route_id NOT IN (SELECT route_id FROM active_routes)
		ORDER BY r.route_id`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RouteSchedule returns trips for a route on a given date, with cancellation
// and actual delay info from transit.stop_delay.
func RouteSchedule(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time) ([]ScheduledTrip, error) {
	rows, err := db.Query(ctx, `
		WITH route_trips AS (
			SELECT tc.trip_id, tc.headsign,
				tc.scheduled_first_dep_time AS start_time,
				tc.scheduled_last_arr_time  AS end_time,
				(SELECT COUNT(*) FROM transit.scheduled_stop ss WHERE ss.trip_id = tc.trip_id) AS stops_total
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE tc.route_id = $1 AND sc.date = $2
		),
		actuals AS (
			SELECT trip_id,
				AVG(COALESCE(arrival_delay, departure_delay))::REAL AS avg_delay,
				COUNT(*) AS stops_observed
			FROM transit.stop_delay
			WHERE route_id = $1 AND date = $2
			GROUP BY trip_id
		)
		SELECT
			rt.trip_id, rt.headsign,
			rt.start_time, rt.end_time,
			CASE WHEN c.trip_id IS NOT NULL THEN TRUE ELSE FALSE END AS canceled,
			a.avg_delay,
			COALESCE(a.stops_observed, 0)::INT,
			rt.stops_total
		FROM route_trips rt
		LEFT JOIN transit.cancellation c
			ON c.trip_id = rt.trip_id
			AND c.feed_timestamp::DATE = $2
		LEFT JOIN actuals a ON a.trip_id = rt.trip_id
		ORDER BY rt.start_time ASC
	`, routeID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ScheduledTrip
	for rows.Next() {
		var s ScheduledTrip
		if err := rows.Scan(
			&s.TripID, &s.Headsign, &s.StartTime, &s.EndTime,
			&s.Canceled, &s.AvgDelay, &s.StopsObserved, &s.StopsTotal,
		); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// ActiveAlert is a service alert currently in effect.
type ActiveAlert struct {
	AlertID        string
	Cause          *string
	Effect         *string
	Header         *string
	Description    *string
	SeverityLevel  *string
	AffectedRoutes []string
	AffectedStops  []string
}

// CurrentAlerts returns alerts present in the most recent feed poll.
func CurrentAlerts(ctx context.Context, db *pgxpool.Pool) ([]ActiveAlert, error) {
	return NewRepo(db).CurrentAlerts(ctx)
}

// RouteInfo holds display info for a route.
type RouteInfo struct {
	RouteID   string
	ShortName string
	LongName  string
	Color     string
	TextColor string
}

// CancelledRoutes returns route IDs with cancellations in the most recent feed poll.
func CancelledRoutes(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	return NewRepo(db).CancelledRoutes(ctx)
}

// CancelledTrip holds details about a single cancelled trip.
type CancelledTrip struct {
	TripID        string `json:"trip_id"`
	RouteID       string `json:"route_id"`
	StartTime     string `json:"start_time"` // e.g. "08:30"
	EndTime       string `json:"end_time"`   // e.g. "09:15"
	Headsign      string `json:"headsign"`
	Upcoming      bool   `json:"upcoming"`       // true if start_time is in the future
	LeadMin       int    `json:"lead_min"`       // minutes of notice before departure (negative = after departure)
	LeadLabel     string `json:"lead_label"`     // "< 5 min after departure", "< 15 min", etc.
	FirstSeen     string `json:"first_seen"`     // "14:32" — when first reported in GTFS-RT feed
	SnapshotCount int    `json:"snapshot_count"` // number of feed polls confirming this cancellation
}

// CancelIncident is a group of consecutive cancelled trips on one route.
type CancelIncident struct {
	RouteID     string
	Trips       []CancelledTrip
	BlockID     string   // non-empty when all trips belong to one block
	BlockRoutes []string // other routes affected by the same block interruption
}

// CancelIncidents groups consecutive cancelled trips on the same route by
// walking the actual schedule. If the next scheduled trip on a route ran,
// the streak breaks and a new incident starts.
func CancelIncidents(ctx context.Context, db *pgxpool.Pool) ([]CancelIncident, error) {
	today := Now()
	todayDate := ServiceDate()
	nowMin := today.Hour()*60 + today.Minute()

	// Get all scheduled trips today with cancellation status + lead time + block.
	rows, err := db.Query(ctx, `
		WITH today_trips AS (
			SELECT tc.trip_id, tc.route_id, tc.headsign, tc.block_id,
			       tc.scheduled_first_dep_time AS start_time
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE sc.date = $1
		),
		cancelled AS (
			SELECT DISTINCT trip_id
			FROM transit.cancellation
			WHERE feed_timestamp = (SELECT MAX(feed_timestamp) FROM transit.cancellation)
		),
		first_seen AS (
			SELECT ec.trip_id,
				EXTRACT(HOUR FROM (MIN(ec.feed_timestamp) AT TIME ZONE 'America/Thunder_Bay')::time)::int * 60 +
				EXTRACT(MINUTE FROM (MIN(ec.feed_timestamp) AT TIME ZONE 'America/Thunder_Bay')::time)::int AS seen_min
			FROM transit.cancellation ec
			JOIN cancelled c ON c.trip_id = ec.trip_id
			GROUP BY ec.trip_id
		)
		SELECT tt.route_id, tt.trip_id, tt.start_time, tt.headsign,
		       CASE WHEN c.trip_id IS NOT NULL THEN TRUE ELSE FALSE END AS is_cancelled,
		       fs.seen_min,
		       tt.block_id
		FROM today_trips tt
		LEFT JOIN cancelled c ON c.trip_id = tt.trip_id
		LEFT JOIN first_seen fs ON fs.trip_id = tt.trip_id
		ORDER BY tt.route_id, tt.start_time
	`, todayDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Group rows by (route, headsign) so we don't mix directions.
	type tripRow struct {
		routeID, tripID, startTime, headsign, blockID string
		cancelled                                     bool
		seenMin                                       *int
	}
	type dirKey struct{ route, headsign string }
	dirTrips := map[dirKey][]tripRow{}
	var dirOrder []dirKey

	for rows.Next() {
		var tr tripRow
		if err := rows.Scan(&tr.routeID, &tr.tripID, &tr.startTime, &tr.headsign, &tr.cancelled, &tr.seenMin, &tr.blockID); err != nil {
			return nil, err
		}
		tr.startTime = trimTime(tr.startTime)
		dk := dirKey{tr.routeID, tr.headsign}
		if _, ok := dirTrips[dk]; !ok {
			dirOrder = append(dirOrder, dk)
		}
		dirTrips[dk] = append(dirTrips[dk], tr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Walk each direction's trips in schedule order, grouping consecutive cancellations.
	type incidentBuild struct {
		incident CancelIncident
		blockIDs map[string]bool // block IDs seen in this incident's cancelled trips
	}
	var builds []incidentBuild
	for _, dk := range dirOrder {
		tt := dirTrips[dk]
		var run []CancelledTrip
		runBlocks := map[string]bool{}

		for _, tr := range tt {
			if tr.cancelled {
				upcoming := false
				var leadMin int
				var h, m int
				if n, _ := fmt.Sscanf(tr.startTime, "%d:%d", &h, &m); n == 2 {
					schedMin := h*60 + m
					upcoming = schedMin > nowMin
					if tr.seenMin != nil {
						leadMin = schedMin - *tr.seenMin
					}
				}
				run = append(run, CancelledTrip{
					TripID:    tr.tripID,
					RouteID:   tr.routeID,
					StartTime: tr.startTime,
					Headsign:  tr.headsign,
					Upcoming:  upcoming,
					LeadMin:   leadMin,
					LeadLabel: leadLabel(leadMin),
				})
				if tr.blockID != "" {
					runBlocks[tr.blockID] = true
				}
			} else if len(run) > 0 {
				builds = append(builds, incidentBuild{
					incident: CancelIncident{RouteID: dk.route, Trips: run},
					blockIDs: runBlocks,
				})
				run = nil
				runBlocks = map[string]bool{}
			}
		}
		if len(run) > 0 {
			builds = append(builds, incidentBuild{
				incident: CancelIncident{RouteID: dk.route, Trips: run},
				blockIDs: runBlocks,
			})
		}
	}

	// Enrich incidents with block context.
	// If all cancelled trips in an incident share one block, set BlockID.
	// Then cross-reference: find other incidents on the same block and list their routes.
	blockToIncidents := map[string][]int{} // block_id → incident indices
	for i := range builds {
		if len(builds[i].blockIDs) == 1 {
			for bid := range builds[i].blockIDs {
				builds[i].incident.BlockID = bid
				blockToIncidents[bid] = append(blockToIncidents[bid], i)
			}
		}
	}
	for bid, idxs := range blockToIncidents {
		if len(idxs) < 2 {
			continue
		}
		// Collect all routes affected by this block interruption.
		routeSet := map[string]bool{}
		for _, idx := range idxs {
			routeSet[builds[idx].incident.RouteID] = true
		}
		_ = bid
		for _, idx := range idxs {
			var others []string
			for r := range routeSet {
				if r != builds[idx].incident.RouteID {
					others = append(others, r)
				}
			}
			sort.Strings(others)
			builds[idx].incident.BlockRoutes = others
		}
	}

	incidents := make([]CancelIncident, len(builds))
	for i := range builds {
		incidents[i] = builds[i].incident
	}

	// If schedule-walk found nothing, fall back to CancelledTripDetails
	// (handles trips with IDs that don't match static GTFS)
	if len(incidents) == 0 {
		details, err := CancelledTripDetails(ctx, db, todayDate)
		if err != nil {
			return nil, err
		}
		for routeID, trips := range details {
			if len(trips) > 0 {
				incidents = append(incidents, CancelIncident{RouteID: routeID, Trips: trips})
			}
		}
	}

	return incidents, nil
}

// CancelledTripDetails returns cancelled trips for a specific service date
// grouped by route, with enriched details from the trips table.
//
// If date is zero, uses ServiceDate() (today). Historical route page views
// must pass their displayed date so the historical cancellation list
// matches the displayed schedule instead of spilling today's cancellations
// onto a week-old view.
func CancelledTripDetails(ctx context.Context, db *pgxpool.Pool, date time.Time) (map[string][]CancelledTrip, error) {
	if date.IsZero() {
		date = ServiceDate()
	}
	now := Now()
	nowMin := now.Hour()*60 + now.Minute()
	if now.Hour() < ServiceDayCutoffHour {
		// Before cutoff, we're in the previous day's service — shift nowMin
		// past 24:00 so upcoming/past comparison works for late-night trips.
		nowMin += 24 * 60
	}
	svcDate := date.Format("20060102")

	// Join to get the earliest feed_timestamp per trip (when the cancellation first appeared).
	// Filter by service date so after midnight we still see the current service day's cancellations.
	rows, err := db.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (trip_id) trip_id, route_id, start_time
			FROM transit.cancellation
			WHERE start_date = $1
			ORDER BY trip_id, feed_timestamp DESC
		),
		first_seen AS (
			SELECT ec.trip_id, MIN(ec.feed_timestamp) AS first_feed,
				COUNT(*) AS snapshot_count
			FROM transit.cancellation ec
			WHERE ec.start_date = $1
			GROUP BY ec.trip_id
		)
		SELECT l.route_id, l.trip_id, l.start_time, COALESCE(tc.headsign, ''),
			COALESCE(tc.scheduled_last_arr_time, '') AS end_time,
			EXTRACT(HOUR FROM (fs.first_feed AT TIME ZONE 'America/Thunder_Bay')::time)::int * 60 +
			EXTRACT(MINUTE FROM (fs.first_feed AT TIME ZONE 'America/Thunder_Bay')::time)::int AS seen_min,
			TO_CHAR(fs.first_feed AT TIME ZONE 'America/Thunder_Bay', 'HH24:MI') AS seen_time,
			COALESCE(fs.snapshot_count, 0)
		FROM latest l
		LEFT JOIN transit.trip_catalog tc ON tc.trip_id = l.trip_id
		LEFT JOIN first_seen fs ON fs.trip_id = l.trip_id
		ORDER BY l.route_id, l.start_time
	`, svcDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]CancelledTrip)
	for rows.Next() {
		var ct CancelledTrip
		var seenMin *int
		var seenTime *string
		if err := rows.Scan(&ct.RouteID, &ct.TripID, &ct.StartTime, &ct.Headsign, &ct.EndTime, &seenMin, &seenTime, &ct.SnapshotCount); err != nil {
			return nil, err
		}
		ct.StartTime = trimTime(ct.StartTime)
		ct.EndTime = trimTime(ct.EndTime)
		if seenTime != nil {
			ct.FirstSeen = *seenTime
		}
		if len(ct.StartTime) >= 5 {
			var h, m int
			fmt.Sscanf(ct.StartTime, "%d:%d", &h, &m)
			schedMin := h*60 + m
			ct.Upcoming = schedMin > nowMin
			if seenMin != nil {
				ct.LeadMin = schedMin - *seenMin
				ct.LeadLabel = leadLabel(ct.LeadMin)
			}
		}
		result[ct.RouteID] = append(result[ct.RouteID], ct)
	}
	return result, rows.Err()
}

func leadLabel(min int) string {
	if min < 0 {
		abs := -min
		if abs >= 60 {
			if abs%60 == 0 {
				return fmt.Sprintf("%dh after departure", abs/60)
			}
			return fmt.Sprintf("%dh %dm after departure", abs/60, abs%60)
		}
		return fmt.Sprintf("%dm after departure", abs)
	}
	if min == 0 {
		return "at departure"
	}
	if min >= 60 {
		if min%60 == 0 {
			return fmt.Sprintf("%dh", min/60)
		}
		return fmt.Sprintf("%dh %dm", min/60, min%60)
	}
	return fmt.Sprintf("%dm", min)
}

func formatDuration(secs int) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// GetRoute returns route display info.
func GetRoute(ctx context.Context, db *pgxpool.Pool, routeID string) (*RouteInfo, error) {
	return NewRepo(db).GetRoute(ctx, routeID)
}

// TimepointStop identifies a benchmark stop on a route.
type TimepointStop struct {
	StopID   string
	StopName string
	Sequence int
}

// TripTimepoint is one cell in the schedule grid: a trip's scheduled time and actual delay at a timepoint stop.
type TripTimepoint struct {
	ScheduledTime  string // e.g. "06:30"
	DelaySec       *int   // arrival delay; nil if no actual data
	DepartureDelay *int   // departure delay; nil if no actual data
}

// TimepointTrip is one row in the schedule grid.
type TimepointTrip struct {
	TripID   string
	Headsign string
	Canceled bool
	Stops    []TripTimepoint // one per timepoint stop, in order
}

// TimepointSchedule is the full grid for a route on a given day.
type TimepointSchedule struct {
	Headsign string
	Stops    []TimepointStop
	Trips    []TimepointTrip
}

// DirectionSection is one direction within a unified schedule grid.
type DirectionSection struct {
	Headsign string
	Stops    []TimepointStop
	StopIdx  []int // index into UnifiedSchedule.Trips[].Stops for each stop
}

// UnifiedSchedule is a single grid where all directions share trip columns.
type UnifiedSchedule struct {
	Sections []DirectionSection
	Trips    []TimepointTrip // each trip has len(allStops) cells
}

// RouteTimepointSchedule builds schedule grids for timepoint stops on a route,
// split by direction (headsign) so each direction gets its own stop columns.
func RouteTimepointSchedule(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time) ([]TimepointSchedule, error) {
	// 1. Find distinct directions and a representative trip for each
	dirRows, err := db.Query(ctx, `
		WITH today_trips AS (
			SELECT tc.trip_id, tc.headsign
			FROM transit.trip_catalog tc
			JOIN transit.service_calendar sc ON sc.service_id = tc.service_id
			WHERE tc.route_id = $1 AND sc.date = $2
		)
		SELECT DISTINCT ON (headsign) headsign, trip_id
		FROM today_trips
		ORDER BY headsign, trip_id
	`, routeID, date)
	if err != nil {
		return nil, err
	}
	defer dirRows.Close()

	type direction struct {
		headsign string
		tripID   string
	}
	var dirs []direction
	for dirRows.Next() {
		var d direction
		if err := dirRows.Scan(&d.headsign, &d.tripID); err != nil {
			return nil, err
		}
		dirs = append(dirs, d)
	}
	if err := dirRows.Err(); err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, nil
	}

	var schedules []TimepointSchedule
	for _, dir := range dirs {
		sched, err := buildDirectionSchedule(ctx, db, routeID, date, dir.headsign, dir.tripID)
		if err != nil {
			return nil, err
		}
		if sched != nil && len(sched.Trips) > 0 {
			schedules = append(schedules, *sched)
		}
	}

	// Merge small directions (< 3 trips) into the best-matching main direction.
	// Singleton trips appear as extra columns with mostly empty cells.
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
			// No main direction to merge into — keep as-is
			main = append(main, s)
			continue
		}
		main[bestIdx] = mergeDirection(main[bestIdx], s)
	}
	return main, nil
}

// findBestMergeTarget returns the index of the main direction sharing the most stops.
func findBestMergeTarget(main []TimepointSchedule, small TimepointSchedule) int {
	if len(main) == 0 {
		return -1
	}
	smallStops := map[string]bool{}
	for _, s := range small.Stops {
		smallStops[s.StopID] = true
	}
	bestIdx, bestOverlap := -1, 0
	for i, m := range main {
		overlap := 0
		for _, s := range m.Stops {
			if smallStops[s.StopID] {
				overlap++
			}
		}
		if overlap > bestOverlap {
			bestOverlap = overlap
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return bestIdx
	}
	return 0 // fallback to first
}

// mergeDirection appends trips from small into target, adding any new stops as rows at the bottom.
func mergeDirection(target, small TimepointSchedule) TimepointSchedule {
	// Build stop index for the target
	stopIdx := map[string]int{}
	for i, s := range target.Stops {
		stopIdx[s.StopID] = i
	}

	// Find new stops in small that target doesn't have
	var newStops []TimepointStop
	for _, s := range small.Stops {
		if _, ok := stopIdx[s.StopID]; !ok {
			stopIdx[s.StopID] = len(target.Stops) + len(newStops)
			newStops = append(newStops, s)
		}
	}
	target.Stops = append(target.Stops, newStops...)

	// Extend existing trips with empty cells for new stops
	if len(newStops) > 0 {
		for i := range target.Trips {
			extra := make([]TripTimepoint, len(newStops))
			target.Trips[i].Stops = append(target.Trips[i].Stops, extra...)
		}
	}

	// Build small's stop mapping
	smallStopIdx := map[string]int{}
	for i, s := range small.Stops {
		smallStopIdx[s.StopID] = i
	}

	// Add each small trip as a new column, mapping stops by ID
	totalStops := len(target.Stops)
	for _, trip := range small.Trips {
		merged := TimepointTrip{
			TripID:   trip.TripID,
			Headsign: trip.Headsign,
			Canceled: trip.Canceled,
			Stops:    make([]TripTimepoint, totalStops),
		}
		for _, ss := range small.Stops {
			srcIdx := smallStopIdx[ss.StopID]
			dstIdx := stopIdx[ss.StopID]
			merged.Stops[dstIdx] = trip.Stops[srcIdx]
		}
		target.Trips = append(target.Trips, merged)
	}

	// Re-sort all trips by their first non-empty time
	sort.Slice(target.Trips, func(i, j int) bool {
		return firstTime(target.Trips[i]) < firstTime(target.Trips[j])
	})

	return target
}

// UnifySchedules merges per-direction schedules into a single grid where all
// trips share the same columns, sorted chronologically. Each direction section
// references its stop rows by index into the unified trip's Stops slice.
func UnifySchedules(schedules []TimepointSchedule) *UnifiedSchedule {
	if len(schedules) == 0 {
		return nil
	}

	// Assign each direction's stops a global index
	var sections []DirectionSection
	globalIdx := 0
	for _, s := range schedules {
		sec := DirectionSection{
			Headsign: s.Headsign,
			Stops:    s.Stops,
			StopIdx:  make([]int, len(s.Stops)),
		}
		for i := range s.Stops {
			sec.StopIdx[i] = globalIdx
			globalIdx++
		}
		sections = append(sections, sec)
	}
	totalStops := globalIdx

	// Build unified trips: each trip has totalStops cells, mostly empty
	var allTrips []TimepointTrip
	for si, s := range schedules {
		sec := sections[si]
		for _, trip := range s.Trips {
			unified := TimepointTrip{
				TripID:   trip.TripID,
				Headsign: trip.Headsign,
				Canceled: trip.Canceled,
				Stops:    make([]TripTimepoint, totalStops),
			}
			for i, tp := range trip.Stops {
				unified.Stops[sec.StopIdx[i]] = tp
			}
			allTrips = append(allTrips, unified)
		}
	}

	// Sort all trips chronologically
	sort.Slice(allTrips, func(i, j int) bool {
		return firstTime(allTrips[i]) < firstTime(allTrips[j])
	})

	return &UnifiedSchedule{Sections: sections, Trips: allTrips}
}

func firstTime(t TimepointTrip) string {
	for _, s := range t.Stops {
		if s.ScheduledTime != "" {
			return s.ScheduledTime
		}
	}
	return "99:99"
}

func buildDirectionSchedule(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, headsign, repTripID string) (*TimepointSchedule, error) {
	// 1. Get timepoint stops from the representative trip
	tpRows, err := db.Query(ctx, `
		SELECT ss.stop_id, COALESCE(s.name, ss.stop_id), ss.stop_sequence
		FROM transit.scheduled_stop ss
		LEFT JOIN transit.stop s ON s.stop_id = ss.stop_id
		WHERE ss.trip_id = $1 AND ss.is_timepoint = true
		ORDER BY ss.stop_sequence
	`, repTripID)
	if err != nil {
		return nil, err
	}
	defer tpRows.Close()

	var stops []TimepointStop
	stopIndex := map[string]int{}
	for tpRows.Next() {
		var s TimepointStop
		if err := tpRows.Scan(&s.StopID, &s.StopName, &s.Sequence); err != nil {
			return nil, err
		}
		stopIndex[s.StopID] = len(stops)
		stops = append(stops, s)
	}
	if err := tpRows.Err(); err != nil {
		return nil, err
	}
	if len(stops) == 0 {
		return nil, nil
	}

	// 2. Get all trips for this direction with timepoint stop times and actual delays
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
			ON ss.trip_id = tc.trip_id AND ss.is_timepoint = true
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
		schedTime = trimTime(schedTime)
		trip.Stops[idx] = TripTimepoint{
			ScheduledTime:  schedTime,
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
