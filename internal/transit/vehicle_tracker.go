package transit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/logger"
)

var trackerLog = logger.New("vehicle_tracker")

// stopLoc holds the lat/lon of a stop.
type stopLoc struct {
	lat, lon float64
}

// tripStopKey identifies a visit-in-progress in memory. The service-date
// component prevents cross-day trip_id collisions at runtime.
type tripStopKey struct {
	tripID string
	stopID string
	date   string // YYYY-MM-DD service date
}

// vehicleState tracks the last-seen state of a vehicle between polls.
type vehicleState struct {
	tripID        string
	currentStopID string
	stopStatus    string
	lat, lon      float64
	timestamp     time.Time
}

// visitInProgress is the in-memory state for a (trip, stop) while the bus is
// still inside the 50m radius. On entry we allocate one of these and INSERT a
// row. On exit we UPDATE the row with exited_at + inside_polls and delete the
// state. See the plan at ~/.claude/plans/indexed-hatching-orbit.md for the
// full state machine.
type visitInProgress struct {
	tripID        string
	stopID        string
	routeID       string
	vehicleID     string
	enteredAt     time.Time
	lastInsideAt  time.Time
	lastInsidePos stopLoc
	minDistance   float64
	insidePolls   int
}

// staleVisitTimeout is how long an activeVisit with no update can linger
// before the sweep finalizes it. Covers the "bus went silent / feed dropped"
// case where no exit event is ever observed.
const staleVisitTimeout = 5 * time.Minute

// sweepEveryNPolls controls how often the stale-visit sweep runs. At ~6s
// poll cadence, 60 polls ≈ 6 minutes, giving us one sweep inside each
// staleVisitTimeout window.
const sweepEveryNPolls = 60

// stopVisitThresholdM is the max distance (meters) to consider a bus as servicing a stop.
// Calibrated from 22K STOPPED_AT observations: P50=11m, P95=48m.
const stopVisitThresholdM = 50.0

// vehicleTracker maintains fleet state and detects stop visits by GPS proximity.
type vehicleTracker struct {
	db *pgxpool.Pool

	mu     sync.Mutex
	states map[string]*vehicleState // keyed by vehicleID

	cacheOnce     sync.Once
	stopLocations map[string]stopLoc
	routeStops    map[string][]string // routeID → stop IDs served by that route
	activeVisits  map[tripStopKey]*visitInProgress
	sweepCounter  int
	tz            *time.Location
}

// stopVisitEntry records a newly-detected visit (INSERT on entry).
type stopVisitEntry struct {
	tripID    string
	stopID    string
	routeID   string
	vehicleID string
	enteredAt time.Time
	distanceM float32
}

// stopVisitExit records a visit finalization (UPDATE on exit).
type stopVisitExit struct {
	tripID      string
	stopID      string
	exitedAt    time.Time
	insidePolls int
	minDistance float32
}

func newVehicleTracker(db *pgxpool.Pool) *vehicleTracker {
	return &vehicleTracker{
		db:           db,
		states:       make(map[string]*vehicleState),
		activeVisits: make(map[tripStopKey]*visitInProgress, 256),
		tz:           TZ,
	}
}

// loadCaches populates stop locations and trip schedules from the database.
func (t *vehicleTracker) loadCaches(ctx context.Context) error {
	var err error
	t.cacheOnce.Do(func() {
		err = t.doLoadCaches(ctx)
	})
	return err
}

func (t *vehicleTracker) doLoadCaches(ctx context.Context) error {
	// Load stop locations
	rows, err := t.db.Query(ctx, "SELECT stop_id, latitude, longitude FROM transit.stop")
	if err != nil {
		return fmt.Errorf("loading stop locations: %w", err)
	}
	defer rows.Close()

	t.stopLocations = make(map[string]stopLoc, 700)
	for rows.Next() {
		var id string
		var loc stopLoc
		if err := rows.Scan(&id, &loc.lat, &loc.lon); err != nil {
			return fmt.Errorf("scanning stop location: %w", err)
		}
		t.stopLocations[id] = loc
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating stop locations: %w", err)
	}

	// Load route → stops mapping (distinct stops per route) from Tier 2
	// pattern tables. This is a tiny cached join that never needs to touch
	// the gtfs.* staging schema.
	rsRows, err := t.db.Query(ctx, `
		SELECT DISTINCT rp.route_id, rps.stop_id
		FROM transit.route_pattern rp
		JOIN transit.route_pattern_stop rps USING (pattern_id)
		ORDER BY rp.route_id, rps.stop_id`)
	if err != nil {
		return fmt.Errorf("loading route stops: %w", err)
	}
	defer rsRows.Close()

	t.routeStops = make(map[string][]string, 20)
	for rsRows.Next() {
		var routeID, stopID string
		if err := rsRows.Scan(&routeID, &stopID); err != nil {
			return fmt.Errorf("scanning route stop: %w", err)
		}
		t.routeStops[routeID] = append(t.routeStops[routeID], stopID)
	}
	if err := rsRows.Err(); err != nil {
		return fmt.Errorf("iterating route stops: %w", err)
	}

	trackerLog.Info("caches loaded", "stops", len(t.stopLocations), "route_stops", len(t.routeStops))
	return nil
}

// processPositions handles a batch of vehicle positions from a feed poll.
// Runs the entry/in-progress/exit state machine for the 50m proximity window
// around each route stop. On entry we INSERT a stop_visit row; while inside
// we update in-memory state; on exit we UPDATE the row with exited_at and
// inside_polls. See ~/.claude/plans/indexed-hatching-orbit.md for the full
// state machine walkthrough.
func (t *vehicleTracker) processPositions(ctx context.Context, positions []VehiclePosition, feedTS time.Time) error {
	if err := t.loadCaches(ctx); err != nil {
		return fmt.Errorf("cache: %w", err)
	}

	if len(positions) == 0 {
		return nil
	}

	// 1. Batch upsert vehicles table
	if err := t.upsertVehicles(ctx, positions, feedTS); err != nil {
		trackerLog.Warn("upsert vehicles", "err", err)
	}

	// 2. Batch insert vehicle assignments
	if err := t.insertAssignments(ctx, positions, feedTS); err != nil {
		trackerLog.Warn("insert assignments", "err", err)
	}

	// 3. State-machine detection. One loop that (a) captures prev state,
	// (b) handles trip-change finalization for the old trip, (c) runs the
	// entry/in-progress/exit logic for the current trip's candidate stops,
	// (d) updates the vehicle state for the next poll.
	t.mu.Lock()
	defer t.mu.Unlock()

	var entries []stopVisitEntry
	var exits []stopVisitExit
	serviceDate := ServiceDate().Format("2006-01-02")

	for i := range positions {
		p := &positions[i]
		if p.VehicleID == "" {
			continue
		}

		// prev holds the previous poll's state for this vehicle, captured
		// BEFORE we overwrite it. This is the segment-interp anchor for
		// the current poll.
		prev := t.states[p.VehicleID]

		// Trip-change finalization: if the vehicle just switched trips,
		// close out any still-active visits that belonged to the old trip.
		if prev != nil && p.TripID != nil && prev.tripID != "" && prev.tripID != *p.TripID {
			t.finalizeVisitsForTrip(p.VehicleID, prev.tripID, &exits)
		}

		// Update vehicle state to the current poll for the next iteration.
		newState := &vehicleState{
			lat:       p.Latitude,
			lon:       p.Longitude,
			timestamp: feedTS,
		}
		if p.TripID != nil {
			newState.tripID = *p.TripID
		}
		if p.CurrentStopID != nil {
			newState.currentStopID = *p.CurrentStopID
		}
		if p.StopStatus != nil {
			newState.stopStatus = *p.StopStatus
		}
		t.states[p.VehicleID] = newState

		if p.TripID == nil || p.RouteID == nil {
			continue
		}

		hasPrev := prev != nil && prev.tripID == *p.TripID &&
			prev.lat != 0 && prev.lon != 0

		stops := t.routeStops[*p.RouteID]
		for _, sid := range stops {
			loc, ok := t.stopLocations[sid]
			if !ok {
				continue
			}

			key := tripStopKey{*p.TripID, sid, serviceDate}
			active := t.activeVisits[key]

			// Point distance: is the current poll inside the radius?
			currDist := haversineMeters(p.Latitude, p.Longitude, loc.lat, loc.lon)
			inside := currDist <= stopVisitThresholdM

			// Segment grazing check only applies on entry (no active visit)
			// when the current point is outside. It catches stops the bus
			// passed between polls. We treat grazing as an entry whose
			// enteredAt is the segment-interp crossing time, then the next
			// poll that's also outside will exit it (making a drive-by).
			if !inside && active == nil && hasPrev {
				segDist, _ := segmentDistToPoint(
					prev.lat, prev.lon, p.Latitude, p.Longitude,
					loc.lat, loc.lon,
				)
				if segDist <= stopVisitThresholdM {
					inside = true
					currDist = segDist
				}
			}

			switch {
			case inside && active == nil:
				// Entry event.
				enteredAt := feedTS
				if hasPrev {
					// Precise boundary crossing from outside → inside.
					if frac, ok := segmentCircleCrossing(
						prev.lat, prev.lon, p.Latitude, p.Longitude,
						loc.lat, loc.lon, stopVisitThresholdM,
					); ok {
						elapsed := feedTS.Sub(prev.timestamp)
						enteredAt = prev.timestamp.Add(time.Duration(frac * float64(elapsed)))
					}
				}
				t.activeVisits[key] = &visitInProgress{
					tripID:        *p.TripID,
					stopID:        sid,
					routeID:       *p.RouteID,
					vehicleID:     p.VehicleID,
					enteredAt:     enteredAt,
					lastInsideAt:  feedTS,
					lastInsidePos: stopLoc{p.Latitude, p.Longitude},
					minDistance:   currDist,
					insidePolls:   1,
				}
				entries = append(entries, stopVisitEntry{
					tripID:    *p.TripID,
					stopID:    sid,
					routeID:   *p.RouteID,
					vehicleID: p.VehicleID,
					enteredAt: enteredAt,
					distanceM: float32(currDist),
				})
			case inside && active != nil:
				// In-progress: still inside the circle, accumulate state.
				active.insidePolls++
				active.lastInsideAt = feedTS
				active.lastInsidePos = stopLoc{p.Latitude, p.Longitude}
				if currDist < active.minDistance {
					active.minDistance = currDist
				}
			case !inside && active != nil:
				// Exit event. Segment-interp the precise moment the bus
				// crossed the boundary from last-inside-pos to current pos.
				exitedAt := active.lastInsideAt
				if frac, ok := segmentCircleCrossing(
					active.lastInsidePos.lat, active.lastInsidePos.lon,
					p.Latitude, p.Longitude,
					loc.lat, loc.lon, stopVisitThresholdM,
				); ok {
					elapsed := feedTS.Sub(active.lastInsideAt)
					exitedAt = active.lastInsideAt.Add(time.Duration(frac * float64(elapsed)))
				}
				exits = append(exits, stopVisitExit{
					tripID:      active.tripID,
					stopID:      active.stopID,
					exitedAt:    exitedAt,
					insidePolls: active.insidePolls,
					minDistance: float32(active.minDistance),
				})
				delete(t.activeVisits, key)
			}
		}
	}

	// 4. Flush entries, then exits. Order matters: a drive-by grazing
	// detection emits an entry on poll N and the next poll's exit for the
	// same (trip, stop) goes through the exits batch below. Both operate
	// on the same row; the INSERT must land first.
	if len(entries) > 0 {
		if err := t.insertStopVisitEntries(ctx, entries); err != nil {
			trackerLog.Warn("insert stop visit entries", "err", err)
		}
	}
	if len(exits) > 0 {
		if err := t.updateStopVisitExits(ctx, exits); err != nil {
			trackerLog.Warn("update stop visit exits", "err", err)
		}
	}

	// 5. Stale-visit sweep every sweepEveryNPolls. Finalizes any active
	// visit whose lastInsideAt is older than staleVisitTimeout.
	t.sweepCounter++
	if t.sweepCounter >= sweepEveryNPolls {
		t.sweepCounter = 0
		t.sweepStaleVisits(ctx, feedTS)
	}

	return nil
}

// finalizeVisitsForTrip closes out any activeVisits for a given vehicle+trip
// combo. Called when a vehicle's tripID changes between polls. No segment
// interpolation is available (we don't know where the bus was between the
// last visit update and the trip change), so exited_at = lastInsideAt.
func (t *vehicleTracker) finalizeVisitsForTrip(vehicleID, oldTripID string, exits *[]stopVisitExit) {
	for key, v := range t.activeVisits {
		if v.vehicleID != vehicleID || v.tripID != oldTripID {
			continue
		}
		*exits = append(*exits, stopVisitExit{
			tripID:      v.tripID,
			stopID:      v.stopID,
			exitedAt:    v.lastInsideAt,
			insidePolls: v.insidePolls,
			minDistance: float32(v.minDistance),
		})
		delete(t.activeVisits, key)
	}
}

// sweepStaleVisits finalizes active visits that haven't been touched in
// staleVisitTimeout. Handles the "vehicle went silent" and "feed dropped"
// cases where no exit event ever arrives. Caller holds t.mu.
func (t *vehicleTracker) sweepStaleVisits(ctx context.Context, now time.Time) {
	var exits []stopVisitExit
	for key, v := range t.activeVisits {
		if now.Sub(v.lastInsideAt) < staleVisitTimeout {
			continue
		}
		exits = append(exits, stopVisitExit{
			tripID:      v.tripID,
			stopID:      v.stopID,
			exitedAt:    v.lastInsideAt,
			insidePolls: v.insidePolls,
			minDistance: float32(v.minDistance),
		})
		delete(t.activeVisits, key)
	}
	if len(exits) == 0 {
		return
	}
	trackerLog.Info("stale visit sweep", "count", len(exits))
	if err := t.updateStopVisitExits(ctx, exits); err != nil {
		trackerLog.Warn("stale sweep update", "err", err)
	}
}

func (t *vehicleTracker) upsertVehicles(ctx context.Context, positions []VehiclePosition, feedTS time.Time) error {
	batch := &pgx.Batch{}
	for i := range positions {
		p := &positions[i]
		if p.VehicleID == "" {
			continue
		}
		batch.Queue(
			`INSERT INTO transit.vehicle (vehicle_id, first_seen, last_seen)
			 VALUES ($1, $2, $2)
			 ON CONFLICT (vehicle_id) DO UPDATE SET last_seen = EXCLUDED.last_seen`,
			p.VehicleID, feedTS,
		)
	}
	if batch.Len() == 0 {
		return nil
	}
	br := t.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch upsert vehicles: %w", err)
		}
	}
	return nil
}

func (t *vehicleTracker) insertAssignments(ctx context.Context, positions []VehiclePosition, feedTS time.Time) error {
	batch := &pgx.Batch{}
	for i := range positions {
		p := &positions[i]
		if p.VehicleID == "" || p.TripID == nil || *p.TripID == "" {
			continue
		}
		routeID := ""
		if p.RouteID != nil {
			routeID = *p.RouteID
		}
		batch.Queue(
			`INSERT INTO transit.vehicle_assignment (date, vehicle_id, trip_id, route_id, started_at)
			 VALUES ($5, $1, $2, $3, $4)
			 ON CONFLICT (date, vehicle_id, trip_id) DO NOTHING`,
			p.VehicleID, *p.TripID, routeID, feedTS, ServiceDate(),
		)
	}
	if batch.Len() == 0 {
		return nil
	}
	br := t.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch insert assignments: %w", err)
		}
	}
	return nil
}

// insertStopVisitEntries INSERTs new visits detected on entry. observed_at
// is set to enteredAt and never rewritten — downstream consumers of
// observed_at (recipes/headway.go, chunk.go, repo.go StopAnalytics) see the
// same first-touch semantics they always have, with no drift. The new
// entered_at column is populated too for the audit page.
func (t *vehicleTracker) insertStopVisitEntries(ctx context.Context, entries []stopVisitEntry) error {
	batch := &pgx.Batch{}
	for _, e := range entries {
		batch.Queue(
			`INSERT INTO transit.stop_visit
				(trip_id, stop_id, route_id, vehicle_id, observed_at, distance_m, entered_at, inside_polls)
			 VALUES ($1, $2, $3, $4, $5, $6, $5, 1)
			 ON CONFLICT (trip_id, stop_id) DO NOTHING`,
			e.tripID, e.stopID, e.routeID,
			e.vehicleID, e.enteredAt, e.distanceM,
		)
	}
	br := t.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch insert stop visit entries: %w", err)
		}
	}
	return nil
}

// updateStopVisitExits UPDATEs finalized visits with exited_at, inside_polls,
// and the minimum distance observed during the visit window. Guarded by
// `exited_at IS NULL` so a re-finalization (e.g., across a process restart)
// cannot overwrite an already-finalized row.
func (t *vehicleTracker) updateStopVisitExits(ctx context.Context, exits []stopVisitExit) error {
	batch := &pgx.Batch{}
	for _, x := range exits {
		batch.Queue(
			`UPDATE transit.stop_visit
			 SET exited_at = $1, inside_polls = $2, distance_m = $3
			 WHERE trip_id = $4 AND stop_id = $5 AND exited_at IS NULL`,
			x.exitedAt, x.insidePolls, x.minDistance,
			x.tripID, x.stopID,
		)
	}
	br := t.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch update stop visit exits: %w", err)
		}
	}
	return nil
}

// haversineMeters computes the great-circle distance between two points in meters.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadius * c
}

// segmentCircleCrossing finds the fraction t in [0,1] along segment A→B where
// the segment crosses a circle centered at P with radius r (meters).
//
// Used by the visit state machine for precise boundary-crossing timestamps:
//   - Entry: A outside, B inside → returns t where the segment enters.
//   - Exit:  A inside, B outside → returns t where the segment leaves.
//
// When both endpoints are on the same side of the circle with no crossing,
// returns ok=false. The caller should check both-inside and both-outside
// externally (this function does not distinguish "miss" from "both inside").
//
// Uses the same flat-Earth Cartesian frame as segmentDistToPoint — accurate
// enough at Thunder Bay's latitude for distances under ~1 km.
func segmentCircleCrossing(aLat, aLon, bLat, bLon, pLat, pLon, rM float64) (t float64, ok bool) {
	cosLat := math.Cos(aLat * math.Pi / 180)
	const mPerDegLat = 111_000.0
	mPerDegLon := mPerDegLat * cosLat

	// Segment in local Cartesian frame (meters) with A at origin.
	dx := (bLon - aLon) * mPerDegLon
	dy := (bLat - aLat) * mPerDegLat
	// Vector from P to A.
	fx := (aLon - pLon) * mPerDegLon
	fy := (aLat - pLat) * mPerDegLat

	// Solve |A + t*(B-A) - P|² = r²
	//     = |f + t*d|² = r²
	// → (d·d) t² + 2(f·d) t + (f·f − r²) = 0
	A := dx*dx + dy*dy
	if A == 0 {
		return 0, false // zero-length segment
	}
	B := 2 * (fx*dx + fy*dy)
	C := fx*fx + fy*fy - rM*rM

	disc := B*B - 4*A*C
	if disc < 0 {
		return 0, false // segment doesn't intersect the circle
	}
	sqrtDisc := math.Sqrt(disc)
	t1 := (-B - sqrtDisc) / (2 * A)
	t2 := (-B + sqrtDisc) / (2 * A)

	// Pick the root in [0,1]. For entry (A outside, B inside) that's t1.
	// For exit (A inside, B outside) that's t2. If both roots are in [0,1]
	// the segment chords the circle (both endpoints outside) — return t1 as
	// the entry point, though the caller likely handles this case via the
	// separate segmentDistToPoint closest-approach path.
	if t1 >= 0 && t1 <= 1 {
		return t1, true
	}
	if t2 >= 0 && t2 <= 1 {
		return t2, true
	}
	return 0, false
}

// segmentDistToPoint returns the minimum distance (meters) from point P to the
// line segment A→B, and the fraction [0,1] along the segment where the closest
// point lies. Uses a flat-Earth approximation with latitude-adjusted longitude
// (accurate enough for distances under ~1 km at Thunder Bay's latitude).
func segmentDistToPoint(aLat, aLon, bLat, bLon, pLat, pLon float64) (distM float64, fraction float64) {
	// Convert to a local Cartesian frame (meters) centered on A.
	// At ~48.4°N, 1° lon ≈ 74.1 km, 1° lat ≈ 111.0 km.
	cosLat := math.Cos(aLat * math.Pi / 180)
	const mPerDegLat = 111_000.0
	mPerDegLon := mPerDegLat * cosLat

	ax, ay := 0.0, 0.0
	bx := (bLon - aLon) * mPerDegLon
	by := (bLat - aLat) * mPerDegLat
	px := (pLon - aLon) * mPerDegLon
	py := (pLat - aLat) * mPerDegLat

	// Project P onto line A→B.
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy
	if lenSq == 0 {
		// A and B are the same point.
		return math.Sqrt(px*px + py*py), 0
	}

	t := ((px-ax)*dx + (py-ay)*dy) / lenSq
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	closestX := ax + t*dx
	closestY := ay + t*dy
	ex := px - closestX
	ey := py - closestY
	return math.Sqrt(ex*ex + ey*ey), t
}
