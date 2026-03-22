package transit

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// LatLng is a geographic coordinate.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// RaptorStopTime is a single stop visit within a trip.
type RaptorStopTime struct {
	StopID    string
	Arrival   int // seconds since midnight
	Departure int // seconds since midnight
}

// RaptorTrip is a complete trip with ordered stop times.
type RaptorTrip struct {
	TripID   string
	RouteID  string
	Headsign string
	Stops    []RaptorStopTime
	stopIdx  map[string]int // stopID → index in Stops
}

// StopMeta holds display info for a stop.
type StopMeta struct {
	Name string
	Lat  float64
	Lon  float64
}

// RouteMeta holds display info for a route.
type RouteMeta struct {
	ShortName string
	LongName  string
	Color     string
}

// Transfer is a precomputed foot-path between two nearby stops.
type Transfer struct {
	ToStop  string
	WalkSec int
}

// Timetable holds all precomputed data for RAPTOR queries on a service day.
type Timetable struct {
	Date       time.Time
	RouteTrips map[string][]*RaptorTrip // routeID → trips sorted by first departure
	StopRoutes map[string][]string      // stopID → routeIDs serving it
	StopInfo   map[string]StopMeta      // stopID → metadata
	RouteInfo  map[string]RouteMeta     // routeID → metadata
	Transfers  map[string][]Transfer    // stopID → walkable nearby stops within 400m
}

// ---------------------------------------------------------------------------
// Result types
// ---------------------------------------------------------------------------

// PlanResult is the top-level trip plan response.
type PlanResult struct {
	Itineraries []Itinerary `json:"itineraries"`
}

// Itinerary is one complete journey option.
type Itinerary struct {
	Departure         string   `json:"departure"`
	Arrival           string   `json:"arrival"`
	DurationMin       int      `json:"duration_min"`
	Transfers         int      `json:"transfers"`
	LeaveBy           string   `json:"leave_by,omitempty"`        // when to leave to catch the first bus
	Label             string   `json:"label,omitempty"`           // e.g. "Next departure"
	NextDepartures    []string `json:"next_departures,omitempty"` // subsequent departure times for the same route combo
	Legs              []Leg    `json:"legs"`
	Cancelled         bool     `json:"cancelled,omitempty"`           // uses a cancelled trip
	CancelledSavedMin int      `json:"cancelled_saved_min,omitempty"` // minutes faster vs best available
}

// Leg is one segment — walking or riding transit.
type Leg struct {
	Type        string  `json:"type"` // "walk" or "transit"
	RouteID     string  `json:"route_id,omitempty"`
	RouteName   string  `json:"route_name,omitempty"`
	RouteColor  string  `json:"route_color,omitempty"`
	Headsign    string  `json:"headsign,omitempty"`
	From        LegStop `json:"from"`
	To          LegStop `json:"to"`
	Departure   string  `json:"departure,omitempty"`
	Arrival     string  `json:"arrival,omitempty"`
	DurationMin int     `json:"duration_min"`
	DistanceM   float64 `json:"distance_m,omitempty"`
	NumStops    int     `json:"stops,omitempty"`
	Hint        string  `json:"hint,omitempty"` // contextual tip for the user
}

// LegStop is a location in a leg.
type LegStop struct {
	StopID      string  `json:"stop_id,omitempty"`
	Name        string  `json:"name"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	DestDistM   float64 `json:"dest_distance_m,omitempty"` // distance to final destination
	DestWalkMin int     `json:"dest_walk_min,omitempty"`   // walk time to destination
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	maxRounds      = 4     // max 4 transit legs = 3 transfers
	maxWalkToStopM = 400.0 // meters from origin/destination to stops
	walkSpeedMPS   = 1.2   // ~4.3 km/h
	raptorInfinity = 1 << 30
)

// ---------------------------------------------------------------------------
// Timetable building
// ---------------------------------------------------------------------------

// BuildTimetable loads all GTFS data for a service day into an in-memory
// timetable optimized for RAPTOR queries.
func BuildTimetable(ctx context.Context, repo *Repo, date time.Time) (*Timetable, error) {
	tt := &Timetable{
		Date:       date,
		RouteTrips: make(map[string][]*RaptorTrip),
		StopRoutes: make(map[string][]string),
		StopInfo:   make(map[string]StopMeta),
		RouteInfo:  make(map[string]RouteMeta),
		Transfers:  make(map[string][]Transfer),
	}

	// Load stop metadata
	stops, err := repo.AllStops(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading stops: %w", err)
	}
	for _, s := range stops {
		tt.StopInfo[s.StopID] = StopMeta{Name: s.StopName, Lat: s.Latitude, Lon: s.Longitude}
	}

	// Load route metadata
	routes, err := repo.RouteDisplayInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading routes: %w", err)
	}
	for _, r := range routes {
		tt.RouteInfo[r.RouteID] = RouteMeta{ShortName: r.ShortName, LongName: r.LongName, Color: r.Color}
	}

	// Load all trips + stop_times for active services on this date
	trips, err := repo.TimetableForDate(ctx, date)
	if err != nil {
		return nil, fmt.Errorf("loading timetable: %w", err)
	}

	// Group trips by route+direction — each direction is a separate RAPTOR "route"
	// because trips in different directions have different stop sequences.
	stopRouteSet := make(map[string]map[string]bool) // stopID → set of route keys
	for _, trip := range trips {
		trip.stopIdx = make(map[string]int, len(trip.Stops))
		routeKey := trip.RouteID + "|" + trip.Headsign
		for i, s := range trip.Stops {
			trip.stopIdx[s.StopID] = i
			if stopRouteSet[s.StopID] == nil {
				stopRouteSet[s.StopID] = make(map[string]bool)
			}
			stopRouteSet[s.StopID][routeKey] = true
		}
		tt.RouteTrips[routeKey] = append(tt.RouteTrips[routeKey], trip)
	}

	// Sort trips within each route by departure at first stop
	for _, rTrips := range tt.RouteTrips {
		sort.Slice(rTrips, func(i, j int) bool {
			return rTrips[i].Stops[0].Departure < rTrips[j].Stops[0].Departure
		})
	}

	// Build StopRoutes index (deduplicated)
	for stopID, routeSet := range stopRouteSet {
		routes := make([]string, 0, len(routeSet))
		for r := range routeSet {
			routes = append(routes, r)
		}
		tt.StopRoutes[stopID] = routes
	}

	// Precompute foot-path transfers: all stop pairs within maxWalkToStopM
	stopIDs := make([]string, 0, len(tt.StopInfo))
	for id := range tt.StopInfo {
		stopIDs = append(stopIDs, id)
	}
	for i, idA := range stopIDs {
		metaA := tt.StopInfo[idA]
		for j := i + 1; j < len(stopIDs); j++ {
			idB := stopIDs[j]
			metaB := tt.StopInfo[idB]
			d := haversineMeters(metaA.Lat, metaA.Lon, metaB.Lat, metaB.Lon)
			if d <= maxWalkToStopM {
				sec := realWalkSec(d)
				tt.Transfers[idA] = append(tt.Transfers[idA], Transfer{ToStop: idB, WalkSec: sec})
				tt.Transfers[idB] = append(tt.Transfers[idB], Transfer{ToStop: idA, WalkSec: sec})
			}
		}
	}

	return tt, nil
}

// ---------------------------------------------------------------------------
// Timetable cache
// ---------------------------------------------------------------------------

// TimetableCache holds the current day's timetable with lazy loading.
type TimetableCache struct {
	mu   sync.RWMutex
	tt   *Timetable
	repo *Repo
}

// NewTimetableCache creates a new cache.
func NewTimetableCache(repo *Repo) *TimetableCache {
	return &TimetableCache{repo: repo}
}

// Get returns the timetable for the given date, building it if needed.
func (c *TimetableCache) Get(ctx context.Context, date time.Time) (*Timetable, error) {
	dateOnly := DateOnly(date)

	c.mu.RLock()
	if c.tt != nil && c.tt.Date.Equal(dateOnly) {
		defer c.mu.RUnlock()
		return c.tt, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tt != nil && c.tt.Date.Equal(dateOnly) {
		return c.tt, nil
	}

	tt, err := BuildTimetable(ctx, c.repo, dateOnly)
	if err != nil {
		return nil, err
	}
	c.tt = tt
	return tt, nil
}

// ---------------------------------------------------------------------------
// RAPTOR algorithm
// ---------------------------------------------------------------------------

type raptorLabel struct {
	time      int
	tripID    string // non-empty for transit leg
	routeID   string
	boardStop string // for transit: where we boarded
	walkFrom  string // non-empty for walk transfer; "origin" for initial walk
}

type stopDist struct {
	stopID string
	distM  float64
}

// nearbyStopsPerRoute returns the closest stop for each route within maxM,
// ensuring every nearby route has at least one boarding/alighting point.
func nearbyStopsPerRoute(tt *Timetable, pt LatLng, maxM float64) []stopDist {
	// Find all stops within range
	all := nearbyStopsInMemory(tt, pt, maxM)

	// For each route, keep only the closest stop
	bestPerRoute := make(map[string]stopDist) // routeID → closest stop
	for _, sd := range all {
		routes := tt.StopRoutes[sd.stopID]
		for _, routeID := range routes {
			if prev, ok := bestPerRoute[routeID]; !ok || sd.distM < prev.distM {
				bestPerRoute[routeID] = sd
			}
		}
	}

	// Deduplicate stops (one stop may serve multiple routes)
	seen := make(map[string]bool)
	var result []stopDist
	for _, sd := range bestPerRoute {
		if !seen[sd.stopID] {
			seen[sd.stopID] = true
			result = append(result, sd)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].distM < result[j].distM })
	return result
}

// baseRouteID extracts the original route ID from a route key ("13|headsign" → "13").
func baseRouteID(routeKey string) string {
	if idx := strings.Index(routeKey, "|"); idx >= 0 {
		return routeKey[:idx]
	}
	return routeKey
}

// hasSameRouteTransfer returns true if any two consecutive transit legs use
// the same base route ID (e.g. riding Route 13 one direction then transferring
// back onto Route 13 the other direction).
func hasSameRouteTransfer(itin Itinerary) bool {
	var lastRouteID string
	for _, leg := range itin.Legs {
		if leg.Type != "transit" {
			continue
		}
		if leg.RouteID == lastRouteID {
			return true
		}
		lastRouteID = leg.RouteID
	}
	return false
}

// earliestTrip returns the earliest trip on a route that departs from stopID
// at or after minTime. Trips are sorted by first departure so we can binary
// search-ish. Returns nil if no trip is catchable.
func earliestTrip(trips []*RaptorTrip, stopID string, minTime int, exclude map[string]bool) *RaptorTrip {
	for _, trip := range trips {
		if exclude[trip.TripID] {
			continue
		}
		idx, ok := trip.stopIdx[stopID]
		if !ok {
			continue
		}
		if trip.Stops[idx].Departure >= minTime {
			return trip
		}
	}
	return nil
}

func nearbyStopsInMemory(tt *Timetable, pt LatLng, maxM float64) []stopDist {
	var result []stopDist
	for id, meta := range tt.StopInfo {
		d := haversineMeters(pt.Lat, pt.Lon, meta.Lat, meta.Lon)
		if d <= maxM {
			result = append(result, stopDist{id, d})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].distM < result[j].distM })
	return result
}

// Plan runs the RAPTOR algorithm and returns Pareto-optimal itineraries.
// Faithful to "Round-Based Public Transit Routing" (Delling, Pajor, Werneck 2012).
// fromStop/toStop, if non-empty, pin origin/destination to that exact stop (0m walk).
// excludeTrips, if non-nil, skips trips with those IDs during route scanning.
func Plan(tt *Timetable, origin, dest LatLng, fromStop, toStop string, departSec int, excludeTrips map[string]bool) (*PlanResult, error) {
	// --- Origin / destination stop discovery ---

	var originStops []stopDist
	if fromStop != "" {
		if _, ok := tt.StopInfo[fromStop]; ok {
			originStops = []stopDist{{stopID: fromStop, distM: 0}}
		}
	}
	if len(originStops) == 0 {
		originStops = nearbyStopsInMemory(tt, origin, maxWalkToStopM)
	}

	var destStops []stopDist
	if toStop != "" {
		if _, ok := tt.StopInfo[toStop]; ok {
			destStops = append(destStops, stopDist{stopID: toStop, distM: 0})
		}
		for _, sd := range nearbyStopsInMemory(tt, dest, maxWalkToStopM) {
			if sd.stopID != toStop {
				destStops = append(destStops, sd)
			}
		}
	}
	if len(destStops) == 0 {
		destStops = nearbyStopsInMemory(tt, dest, maxWalkToStopM)
	}

	if len(originStops) == 0 || len(destStops) == 0 {
		return &PlanResult{Itineraries: []Itinerary{}}, nil
	}

	// --- Initialization (paper: τ*(p) = ∞, τ_0(p) = ∞) ---

	nStops := len(tt.StopInfo)

	tauStar := make(map[string]int, nStops) // global best arrival
	tauPrev := make(map[string]int, nStops) // τ_{k-1}(p)
	for id := range tt.StopInfo {
		tauStar[id] = raptorInfinity
		tauPrev[id] = raptorInfinity
	}

	labels := make([]map[string]*raptorLabel, maxRounds+1)
	for k := range labels {
		labels[k] = make(map[string]*raptorLabel)
	}

	// Round 0: walk from origin to all nearby stops
	marked := make(map[string]bool)
	for _, os := range originStops {
		arr := departSec + realWalkSec(os.distM)
		if arr < tauStar[os.stopID] {
			tauStar[os.stopID] = arr
			tauPrev[os.stopID] = arr
			labels[0][os.stopID] = &raptorLabel{time: arr, walkFrom: "origin"}
			marked[os.stopID] = true
		}
	}

	// Foot-path transfers from initial origin stops (paper Step 3 for round 0)
	for stopID := range marked {
		arr := tauPrev[stopID]
		for _, tr := range tt.Transfers[stopID] {
			walkArr := arr + tr.WalkSec
			if walkArr < tauPrev[tr.ToStop] {
				tauPrev[tr.ToStop] = walkArr
				tauStar[tr.ToStop] = walkArr
				labels[0][tr.ToStop] = &raptorLabel{time: walkArr, walkFrom: stopID}
				marked[tr.ToStop] = true
			}
		}
	}

	// --- Rounds 1..maxRounds (paper algorithm) ---

	for k := 1; k <= maxRounds; k++ {
		if len(marked) == 0 {
			break
		}

		// τ_k(p) starts as τ_{k-1}(p)
		tauCurr := make(map[string]int, nStops)
		for id := range tt.StopInfo {
			tauCurr[id] = tauPrev[id]
		}

		newMarked := make(map[string]bool)

		// Step 1: Collect routes serving marked stops, earliest stop per route
		type routeBoard struct {
			routeKey string
			stopSeq  int
		}
		routeSet := map[string]*routeBoard{}
		for stopID := range marked {
			for _, routeKey := range tt.StopRoutes[stopID] {
				trips := tt.RouteTrips[routeKey]
				if len(trips) == 0 {
					continue
				}
				idx, ok := trips[0].stopIdx[stopID]
				if !ok {
					continue
				}
				if rb, exists := routeSet[routeKey]; !exists || idx < rb.stopSeq {
					routeSet[routeKey] = &routeBoard{routeKey: routeKey, stopSeq: idx}
				}
			}
		}

		// Step 2: Traverse each route
		for _, rb := range routeSet {
			trips := tt.RouteTrips[rb.routeKey]
			repTrip := trips[0]

			var currentTrip *RaptorTrip
			var boardStop string

			for si := rb.stopSeq; si < len(repTrip.Stops); si++ {
				stopID := repTrip.Stops[si].StopID

				// Check arrival first (paper order)
				if currentTrip != nil {
					if ctIdx, ok := currentTrip.stopIdx[stopID]; ok {
						arr := currentTrip.Stops[ctIdx].Arrival
						if arr < tauCurr[stopID] && arr < tauStar[stopID] {
							tauCurr[stopID] = arr
							tauStar[stopID] = arr
							labels[k][stopID] = &raptorLabel{
								time:      arr,
								tripID:    currentTrip.TripID,
								routeID:   rb.routeKey,
								boardStop: boardStop,
							}
							newMarked[stopID] = true
						}
					}
				}

				// Can we board an earlier trip? Use τ_{k-1}(p) for boarding
				if tauPrev[stopID] < raptorInfinity {
					et := earliestTrip(trips, stopID, tauPrev[stopID], excludeTrips)
					if et != nil {
						if currentTrip == nil {
							currentTrip = et
							boardStop = stopID
						} else if ctIdx, ok := currentTrip.stopIdx[stopID]; ok {
							etIdx := et.stopIdx[stopID]
							if et.Stops[etIdx].Departure < currentTrip.Stops[ctIdx].Departure {
								currentTrip = et
								boardStop = stopID
							}
						}
					}
				}
			}
		}

		// Step 3: Foot-path transfers (single pass, no cascading)
		markedSlice := make([]string, 0, len(newMarked))
		for stopID := range newMarked {
			markedSlice = append(markedSlice, stopID)
		}
		for _, stopID := range markedSlice {
			arr := tauCurr[stopID]
			for _, tr := range tt.Transfers[stopID] {
				walkArr := arr + tr.WalkSec
				if walkArr < tauCurr[tr.ToStop] && walkArr < tauStar[tr.ToStop] {
					tauCurr[tr.ToStop] = walkArr
					tauStar[tr.ToStop] = walkArr
					labels[k][tr.ToStop] = &raptorLabel{
						time:     walkArr,
						walkFrom: stopID,
					}
					newMarked[tr.ToStop] = true
				}
			}
		}

		tauPrev = tauCurr
		marked = newMarked
	}

	return extractResults(tt, labels, origin, dest, destStops, departSec)
}

// ---------------------------------------------------------------------------
// Result extraction
// ---------------------------------------------------------------------------

type candidate struct {
	stopID  string
	round   int
	arrival int
}

func extractResults(tt *Timetable, labels []map[string]*raptorLabel, origin, dest LatLng, destStops []stopDist, departSec int) (*PlanResult, error) {
	// Find best arrival at each destination stop per round (k >= 1 = at least one transit leg)
	var candidates []candidate
	for _, ds := range destStops {
		walkSec := realWalkSec(ds.distM)
		for k := 1; k <= maxRounds; k++ {
			if lbl, ok := labels[k][ds.stopID]; ok {
				candidates = append(candidates, candidate{ds.stopID, k, lbl.time + walkSec})
			}
		}
	}

	if len(candidates) == 0 {
		return &PlanResult{Itineraries: []Itinerary{}}, nil
	}

	// Best arrival per round — prefer pinned destination stop over nearby alternatives
	pinnedStop := ""
	if len(destStops) > 0 && destStops[0].distM == 0 {
		pinnedStop = destStops[0].stopID
	}
	roundBest := make(map[int]*candidate)
	for i := range candidates {
		c := &candidates[i]
		prev, exists := roundBest[c.round]
		if !exists {
			roundBest[c.round] = c
		} else if c.stopID == pinnedStop && prev.stopID != pinnedStop {
			// Prefer pinned stop even if nearby is faster
			roundBest[c.round] = c
		} else if prev.stopID == pinnedStop && c.stopID != pinnedStop {
			// Keep pinned stop
		} else if c.arrival < prev.arrival {
			roundBest[c.round] = c
		}
	}

	// Keep best per round — show all options so user can choose fewer transfers
	// vs faster arrival. Drop only if strictly dominated (same or more transfers
	// AND same or later arrival).
	var pareto []*candidate
	for k := 1; k <= maxRounds; k++ {
		c, ok := roundBest[k]
		if !ok {
			continue
		}
		// Drop if an earlier round arrives at the same time or earlier
		dominated := false
		for _, prev := range pareto {
			if prev.arrival <= c.arrival {
				dominated = true
				break
			}
		}
		if !dominated {
			pareto = append(pareto, c)
		}
	}

	result := &PlanResult{Itineraries: make([]Itinerary, 0, len(pareto))}
	for _, p := range pareto {
		itin := reconstructPath(tt, labels, origin, dest, p.stopID, p.round, departSec)
		if itin != nil {
			result.Itineraries = append(result.Itineraries, *itin)
		}
	}

	// Prefer itineraries without same-route transfers (e.g. Route 13 → Route 13),
	// but keep them as a fallback if no better option exists.
	{
		var clean, sameRoute []Itinerary
		for _, itin := range result.Itineraries {
			if hasSameRouteTransfer(itin) {
				sameRoute = append(sameRoute, itin)
			} else {
				clean = append(clean, itin)
			}
		}
		if len(clean) > 0 {
			result.Itineraries = clean
		} else {
			result.Itineraries = sameRoute
		}
	}

	// Dedup by route signature (same sequence of route IDs = duplicate)
	seen := map[string]bool{}
	var deduped []Itinerary
	for _, itin := range result.Itineraries {
		var sig string
		for _, leg := range itin.Legs {
			if leg.Type != "walk" {
				sig += leg.RouteID + ">"
			}
		}
		if !seen[sig] {
			seen[sig] = true
			deduped = append(deduped, itin)
		}
	}
	result.Itineraries = deduped

	// Sort by fewest transfers first, then fastest within same transfer count.
	// This prefers the "stable" route people would normally ride.
	sort.Slice(result.Itineraries, func(i, j int) bool {
		if result.Itineraries[i].Transfers != result.Itineraries[j].Transfers {
			return result.Itineraries[i].Transfers < result.Itineraries[j].Transfers
		}
		return result.Itineraries[i].DurationMin < result.Itineraries[j].DurationMin
	})

	// Drop complex options that don't save meaningful time (10+ min) over simpler ones.
	if len(result.Itineraries) > 1 {
		bestSimple := result.Itineraries[0].DurationMin
		var kept []Itinerary
		kept = append(kept, result.Itineraries[0])
		for _, it := range result.Itineraries[1:] {
			if it.Transfers <= result.Itineraries[0].Transfers || bestSimple-it.DurationMin >= 10 {
				kept = append(kept, it)
			}
		}
		result.Itineraries = kept
	}

	// Find next departures for each itinerary's first transit leg
	for i := range result.Itineraries {
		result.Itineraries[i].NextDepartures = findNextDepartures(tt, &result.Itineraries[i], 2)
	}

	return result, nil
}

// findNextDepartures finds the next N departure times for the same route
// from the same boarding stop, after the itinerary's first transit departure.
func findNextDepartures(tt *Timetable, itin *Itinerary, count int) []string {
	// Find the first transit leg
	var firstLeg *Leg
	for i := range itin.Legs {
		if itin.Legs[i].Type != "walk" {
			firstLeg = &itin.Legs[i]
			break
		}
	}
	if firstLeg == nil || firstLeg.RouteID == "" {
		return nil
	}

	// Parse the departure time
	depSec := parseHMS(firstLeg.Departure + ":00")
	if depSec < 0 {
		return nil
	}

	routeKey := firstLeg.RouteID + "|" + firstLeg.Headsign
	trips := tt.RouteTrips[routeKey]
	if len(trips) == 0 {
		return nil
	}

	boardStopID := firstLeg.From.StopID
	var departures []string
	for _, trip := range trips {
		idx, ok := trip.stopIdx[boardStopID]
		if !ok {
			continue
		}
		tripDep := trip.Stops[idx].Departure
		if tripDep > depSec {
			departures = append(departures, fmtTime(tripDep))
			if len(departures) >= count {
				break
			}
		}
	}
	return departures
}

// ---------------------------------------------------------------------------
// Path reconstruction
// ---------------------------------------------------------------------------

func reconstructPath(tt *Timetable, labels []map[string]*raptorLabel, origin, dest LatLng, destStopID string, round, departSec int) *Itinerary {
	// Trace back from destination stop, collecting legs in reverse
	var reversed []Leg
	currentStop := destStopID
	currentRound := round

	for currentRound >= 0 {
		lbl := labels[currentRound][currentStop]
		if lbl == nil {
			break
		}

		if lbl.walkFrom == "origin" {
			break
		}

		if lbl.walkFrom != "" {
			// Walk transfer
			fromMeta := tt.StopInfo[lbl.walkFrom]
			toMeta := tt.StopInfo[currentStop]
			walkM := haversineMeters(fromMeta.Lat, fromMeta.Lon, toMeta.Lat, toMeta.Lon)
			reversed = append(reversed, Leg{
				Type:        "walk",
				From:        LegStop{StopID: lbl.walkFrom, Name: fromMeta.Name, Lat: fromMeta.Lat, Lon: fromMeta.Lon},
				To:          LegStop{StopID: currentStop, Name: toMeta.Name, Lat: toMeta.Lat, Lon: toMeta.Lon},
				DurationMin: walkMin(walkM),
				DistanceM:   math.Round(walkM),
			})
			currentStop = lbl.walkFrom
			continue // same round
		}

		if lbl.tripID != "" {
			// Transit leg
			trip := findTrip(tt, lbl.routeID, lbl.tripID)
			if trip == nil {
				break
			}
			boardIdx := trip.stopIdx[lbl.boardStop]
			alightIdx := trip.stopIdx[currentStop]
			rInfo := tt.RouteInfo[baseRouteID(lbl.routeID)]
			boardMeta := tt.StopInfo[lbl.boardStop]
			alightMeta := tt.StopInfo[currentStop]

			dep := trip.Stops[boardIdx].Departure
			arr := trip.Stops[alightIdx].Arrival

			reversed = append(reversed, Leg{
				Type:        "transit",
				RouteID:     baseRouteID(lbl.routeID),
				RouteName:   rInfo.ShortName,
				RouteColor:  rInfo.Color,
				Headsign:    trip.Headsign,
				From:        LegStop{StopID: lbl.boardStop, Name: boardMeta.Name, Lat: boardMeta.Lat, Lon: boardMeta.Lon},
				To:          LegStop{StopID: currentStop, Name: alightMeta.Name, Lat: alightMeta.Lat, Lon: alightMeta.Lon},
				Departure:   fmtTime(dep),
				Arrival:     fmtTime(arr),
				DurationMin: (arr - dep + 59) / 60,
				NumStops:    alightIdx - boardIdx,
			})
			currentStop = lbl.boardStop
			currentRound--
			continue
		}

		break
	}

	// Build legs in forward order
	var legs []Leg

	// Initial walk: origin → first boarding stop
	originMeta := tt.StopInfo[currentStop]
	originWalkM := haversineMeters(origin.Lat, origin.Lon, originMeta.Lat, originMeta.Lon)
	if originWalkM > 10 {
		legs = append(legs, Leg{
			Type:        "walk",
			From:        LegStop{Name: "Origin", Lat: origin.Lat, Lon: origin.Lon},
			To:          LegStop{StopID: currentStop, Name: originMeta.Name, Lat: originMeta.Lat, Lon: originMeta.Lon},
			DurationMin: walkMin(originWalkM),
			DistanceM:   math.Round(originWalkM),
		})
	}

	// Reversed legs → forward order
	for i := len(reversed) - 1; i >= 0; i-- {
		legs = append(legs, reversed[i])
	}

	// Final walk: last stop → destination
	destMeta := tt.StopInfo[destStopID]
	destWalkM := haversineMeters(destMeta.Lat, destMeta.Lon, dest.Lat, dest.Lon)
	if destWalkM > 10 {
		legs = append(legs, Leg{
			Type:        "walk",
			From:        LegStop{StopID: destStopID, Name: destMeta.Name, Lat: destMeta.Lat, Lon: destMeta.Lon},
			To:          LegStop{Name: "Destination", Lat: dest.Lat, Lon: dest.Lon},
			DurationMin: walkMin(destWalkM),
			DistanceM:   math.Round(destWalkM),
		})
	}

	// Merge consecutive walk legs
	legs = mergeWalkLegs(legs)

	// Enrich transit stops with distance to destination
	for i := range legs {
		if legs[i].Type == "transit" {
			enrichDestDist(&legs[i].From, dest)
			enrichDestDist(&legs[i].To, dest)
		}
	}

	// Add "stay on" hint: if last transit leg gets off but the route continues
	// closer to the destination, tell the user
	addStayOnHint(tt, legs, dest)

	if len(legs) == 0 {
		return nil
	}

	// Compute actual arrival from leg durations (not penalized internal time)
	totalSec := departSec
	for _, l := range legs {
		totalSec += l.DurationMin * 60
	}
	// More precise: use last transit leg's arrival + final walk
	for i := len(legs) - 1; i >= 0; i-- {
		if legs[i].Type == "transit" && legs[i].Arrival != "" {
			arrSec := parseHMS(legs[i].Arrival + ":00")
			finalWalkSec := 0
			for j := i + 1; j < len(legs); j++ {
				finalWalkSec += legs[j].DurationMin * 60
			}
			totalSec = arrSec + finalWalkSec
			break
		}
	}

	transfers := 0
	for _, l := range legs {
		if l.Type == "transit" {
			transfers++
		}
	}
	if transfers > 0 {
		transfers--
	}

	// Compute "leave by" — first transit departure minus initial walk time
	leaveBy := ""
	walkBefore := 0
	for _, l := range legs {
		if l.Type == "walk" {
			walkBefore += l.DurationMin * 60
		} else if l.Type == "transit" && l.Departure != "" {
			leaveSec := parseHMS(l.Departure+":00") - walkBefore
			leaveBy = fmtTime(leaveSec)
			break
		}
	}

	return &Itinerary{
		Departure:   fmtTime(departSec),
		Arrival:     fmtTime(totalSec),
		DurationMin: (totalSec - departSec + 59) / 60,
		Transfers:   transfers,
		LeaveBy:     leaveBy,
		Legs:        legs,
	}
}

// mergeWalkLegs collapses consecutive walk legs into a single leg.
func mergeWalkLegs(legs []Leg) []Leg {
	if len(legs) <= 1 {
		return legs
	}
	merged := []Leg{legs[0]}
	for i := 1; i < len(legs); i++ {
		prev := &merged[len(merged)-1]
		cur := legs[i]
		if prev.Type == "walk" && cur.Type == "walk" {
			prev.To = cur.To
			prev.DistanceM += cur.DistanceM
			prev.DurationMin = walkMin(prev.DistanceM)
		} else {
			merged = append(merged, cur)
		}
	}
	return merged
}

// addStayOnHint checks if the last transit leg's route continues to a stop
// closer to the destination. If so, adds a hint like
// "Or stay on 2 more stops to Stop X (150m from destination, arrives 15:35)"
func addStayOnHint(tt *Timetable, legs []Leg, dest LatLng) {
	// Find the last transit leg
	lastTransitIdx := -1
	for i := len(legs) - 1; i >= 0; i-- {
		if legs[i].Type == "transit" {
			lastTransitIdx = i
			break
		}
	}
	if lastTransitIdx < 0 {
		return
	}

	leg := &legs[lastTransitIdx]
	alightDist := leg.To.DestDistM
	if alightDist == 0 {
		return // already at destination
	}

	// Find the trip to scan remaining stops
	trip := findTrip(tt, leg.RouteID, "")
	// We need the actual trip — search by route and matching board/alight stops
	for _, t := range tt.RouteTrips[leg.RouteID] {
		if _, ok := t.stopIdx[leg.From.StopID]; ok {
			if _, ok2 := t.stopIdx[leg.To.StopID]; ok2 {
				trip = t
				break
			}
		}
	}
	if trip == nil {
		return
	}

	alightIdx, ok := trip.stopIdx[leg.To.StopID]
	if !ok {
		return
	}

	// Scan remaining stops on the trip after the alight point
	bestStopID := ""
	bestDist := alightDist
	bestArr := 0
	for i := alightIdx + 1; i < len(trip.Stops); i++ {
		s := trip.Stops[i]
		meta, ok := tt.StopInfo[s.StopID]
		if !ok {
			continue
		}
		d := math.Round(haversineMeters(meta.Lat, meta.Lon, dest.Lat, dest.Lon))
		if d < bestDist-50 { // at least 50m closer to be worth mentioning
			bestDist = d
			bestStopID = s.StopID
			bestArr = s.Arrival
		}
	}

	if bestStopID == "" {
		return
	}

	stopMeta := tt.StopInfo[bestStopID]
	stopsMore := 0
	if idx, ok := trip.stopIdx[bestStopID]; ok {
		stopsMore = idx - alightIdx
	}
	leg.Hint = fmt.Sprintf("Or stay on %d more stop%s to %s (%sm from destination, arrives %s)",
		stopsMore, pluralS(stopsMore), stopMeta.Name, fmtDistM(bestDist), fmtTime(bestArr))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func fmtDistM(m float64) string {
	if m < 1000 {
		return fmt.Sprintf("%.0f", m)
	}
	return fmt.Sprintf("%.1fk", m/1000)
}

func enrichDestDist(stop *LegStop, dest LatLng) {
	if stop.StopID == "" {
		return
	}
	d := haversineMeters(stop.Lat, stop.Lon, dest.Lat, dest.Lon)
	stop.DestDistM = math.Round(d)
	stop.DestWalkMin = walkMin(d)
}

func findTrip(tt *Timetable, routeID, tripID string) *RaptorTrip {
	for _, t := range tt.RouteTrips[routeID] {
		if t.TripID == tripID {
			return t
		}
	}
	return nil
}

// realWalkSec returns walk time in seconds.
func realWalkSec(distM float64) int {
	return int(math.Ceil(distM / walkSpeedMPS))
}

func walkMin(meters float64) int {
	m := int(math.Ceil(meters / walkSpeedMPS / 60))
	if m < 1 {
		m = 1
	}
	return m
}

func fmtTime(sec int) string {
	sec = ((sec % 86400) + 86400) % 86400
	return fmt.Sprintf("%02d:%02d", sec/3600, (sec%3600)/60)
}
