package transit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Reporter generates transit reports from event data.
// Each method assembles a complete report — the handler just picks the output format.
type Reporter struct {
	db      *pgxpool.Pool
	repo    *Repo
	client  *Client
	ttCache *TimetableCache
}

// NewReporter creates a reporter with its dependencies.
func NewReporter(db *pgxpool.Pool, client *Client) *Reporter {
	repo := NewRepo(db)
	return &Reporter{
		db:      db,
		repo:    repo,
		client:  client,
		ttCache: NewTimetableCache(repo),
	}
}

// --- Dashboard ---

// DashboardReport is the full transit overview page data.
type DashboardReport struct {
	Alerts         []ActiveAlert
	CancelledTrips map[string][]CancelledTrip
	FleetSize      int
}

// Dashboard assembles the transit overview: alerts, cancellations.
func (r *Reporter) Dashboard(ctx context.Context) (*DashboardReport, error) {
	alerts, err := r.repo.CurrentAlerts(ctx)
	if err != nil {
		alerts = nil
	}

	cancelledTrips, err := CancelledTripDetails(ctx, r.db, ServiceDate())
	if err != nil {
		cancelledTrips = make(map[string][]CancelledTrip)
	}

	fleetSize, _ := r.repo.FleetSize(ctx)

	return &DashboardReport{
		Alerts:         alerts,
		CancelledTrips: cancelledTrips,
		FleetSize:      fleetSize,
	}, nil
}

// CancelIncidents returns schedule-aware consecutive cancellation groups.
func (r *Reporter) CancelIncidents(ctx context.Context) ([]CancelIncident, error) {
	return CancelIncidents(ctx, r.db)
}

// --- Stats ---

// StatsReport holds one of three stats views: day snapshots, percentiles, or week summary.
type StatsReport struct {
	Type      string                  `json:"type"`
	Snapshots []TransitSnapshot       `json:"snapshots,omitempty"`
	Buckets   []DelayPercentileBucket `json:"buckets,omitempty"`
	Days      []DaySummary            `json:"days,omitempty"`
}

// DayStats returns 24h of 5-minute system snapshots derived from events.
func (r *Reporter) DayStats(ctx context.Context) (*StatsReport, error) {
	snapshots, err := r.repo.DaySnapshots(ctx)
	if err != nil {
		return nil, err
	}
	if snapshots == nil {
		snapshots = []TransitSnapshot{}
	}
	return &StatsReport{Type: "day", Snapshots: snapshots}, nil
}

// Percentiles returns 24h delay percentile buckets.
func (r *Reporter) Percentiles(ctx context.Context) (*StatsReport, error) {
	buckets, err := r.repo.DayPercentiles(ctx)
	if err != nil {
		return nil, err
	}
	if buckets == nil {
		buckets = []DelayPercentileBucket{}
	}
	return &StatsReport{Type: "percentiles", Buckets: buckets}, nil
}

// WeekStats returns daily aggregates for the last 7 days.
func (r *Reporter) WeekStats(ctx context.Context) (*StatsReport, error) {
	days, err := r.repo.WeekSummary(ctx)
	if err != nil {
		return nil, err
	}
	if days == nil {
		days = []DaySummary{}
	}
	return &StatsReport{Type: "week", Days: days}, nil
}

// --- Live Data ---

// StopPredictionsReport returns upcoming arrivals for a stop.
func (r *Reporter) StopPredictionsReport(ctx context.Context, stopID string) (StopPredictionsResponse, error) {
	resp, err := StopPredictions(ctx, r.db, r.client, stopID, Now())
	if err != nil {
		return StopPredictionsResponse{}, err
	}
	if resp.Predictions == nil {
		resp.Predictions = []StopPrediction{}
	}
	return resp, nil
}

// AllStopsReport returns every stop with valid coordinates.
func (r *Reporter) AllStopsReport(ctx context.Context) ([]Stop, error) {
	stops, err := r.repo.AllStops(ctx)
	if err != nil {
		return nil, err
	}
	if stops == nil {
		stops = []Stop{}
	}
	return stops, nil
}

// --- Trip Planner ---

// TripPlan runs the RAPTOR algorithm to find transit itineraries.
// It runs twice: once excluding cancelled trips (available routes), and once
// with all trips (ideal routes). Cancelled itineraries that would be faster
// are included with a flag and the time delta.
func (r *Reporter) TripPlan(ctx context.Context, origin, dest LatLng, fromStop, toStop string, departSec int, date time.Time) (*PlanResult, error) {
	tt, err := r.ttCache.Get(ctx, date)
	if err != nil {
		return nil, err
	}

	// Fetch currently cancelled trips (fresh, not cached)
	cancelled, err := r.repo.CurrentCancelledTripIDs(ctx)
	if err != nil {
		cancelled = nil // degrade gracefully
	}

	// Run with cancelled trips excluded → available routes
	available, err := Plan(tt, origin, dest, fromStop, toStop, departSec, cancelled)
	if err != nil {
		return nil, err
	}

	// If no cancellations, just return available
	if len(cancelled) == 0 {
		return available, nil
	}

	// Run with all trips → ideal routes
	ideal, err := Plan(tt, origin, dest, fromStop, toStop, departSec, nil)
	if err != nil {
		return available, nil // degrade gracefully
	}

	// Find best available arrival for comparison
	bestAvailableMin := 0
	if len(available.Itineraries) > 0 {
		bestAvailableMin = available.Itineraries[0].DurationMin
		for _, it := range available.Itineraries {
			if it.DurationMin < bestAvailableMin {
				bestAvailableMin = it.DurationMin
			}
		}
	}

	// Check if any ideal itinerary is meaningfully faster (>= 3 min saved)
	for _, idealIt := range ideal.Itineraries {
		saved := bestAvailableMin - idealIt.DurationMin
		if saved >= 3 {
			cancelledIt := idealIt
			cancelledIt.Cancelled = true
			cancelledIt.CancelledSavedMin = saved
			available.Itineraries = append(available.Itineraries, cancelledIt)
			break // only show the single best cancelled option
		}
	}

	// Find next departure option: run RAPTOR again starting after the first bus
	if len(available.Itineraries) > 0 {
		firstDep := firstTransitDeparture(available.Itineraries[0])
		if firstDep > 0 {
			nextResult, err := Plan(tt, origin, dest, fromStop, toStop, firstDep+60, cancelled)
			if err == nil && len(nextResult.Itineraries) > 0 {
				next := nextResult.Itineraries[0]
				// Only show if it's a meaningfully different departure
				if next.Departure != available.Itineraries[0].Departure {
					next.Label = "Next departure"
					available.Itineraries = append(available.Itineraries, next)
				}
			}
		}
	}

	return available, nil
}

// TripPlanArriveBy finds the latest departure that arrives before arriveSec.
// Uses binary search over forward RAPTOR runs.
func (r *Reporter) TripPlanArriveBy(ctx context.Context, origin, dest LatLng, fromStop, toStop string, arriveSec int, date time.Time) (*PlanResult, error) {
	tt, err := r.ttCache.Get(ctx, date)
	if err != nil {
		return nil, err
	}

	cancelled, err := r.repo.CurrentCancelledTripIDs(ctx)
	if err != nil {
		cancelled = nil
	}

	// Binary search: find latest departure where best arrival <= arriveSec
	lo := arriveSec - 7200 // 2 hours before
	if lo < 0 {
		lo = 0
	}
	hi := arriveSec
	var bestResult *PlanResult

	for lo <= hi {
		mid := (lo + hi) / 2
		result, err := Plan(tt, origin, dest, fromStop, toStop, mid, cancelled)
		if err != nil || len(result.Itineraries) == 0 {
			// No route at this departure — try earlier
			hi = mid - 60
			continue
		}

		// Check if best itinerary arrives on time
		best := result.Itineraries[0]
		arrSec := parseHMS(best.Arrival + ":00")
		if arrSec <= arriveSec {
			bestResult = result
			lo = mid + 60 // try later departure
		} else {
			hi = mid - 60 // arrives too late, try earlier
		}
	}

	if bestResult == nil {
		return &PlanResult{Itineraries: []Itinerary{}}, nil
	}

	return bestResult, nil
}

// firstTransitDeparture returns the departure time (seconds since midnight)
// of the first transit leg in an itinerary, or 0 if none.
func firstTransitDeparture(it Itinerary) int {
	for _, leg := range it.Legs {
		if leg.Type == "transit" && leg.Departure != "" {
			return parseHMS(leg.Departure + ":00")
		}
	}
	return 0
}

// --- Spatial ---

// NearestStopsReport returns the closest stops to a location.
func (r *Reporter) NearestStopsReport(ctx context.Context, lat, lon float64, limit int) ([]StopWithDistance, error) {
	stops, err := r.repo.NearestStops(ctx, lat, lon, limit)
	if err != nil {
		return nil, err
	}
	if stops == nil {
		stops = []StopWithDistance{}
	}
	return stops, nil
}

// VehicleDistanceReport returns a vehicle's distance to a specific stop.
func (r *Reporter) VehicleDistanceReport(ctx context.Context, vehicleID, stopID string) (*VehicleDistance, error) {
	return r.repo.VehicleDistanceToStop(ctx, vehicleID, stopID)
}

// VehicleFeedRaw returns the raw GTFS-RT vehicle positions protobuf.
func (r *Reporter) VehicleFeedRaw(ctx context.Context) ([]byte, error) {
	return r.client.FetchVehiclesRaw(ctx)
}
