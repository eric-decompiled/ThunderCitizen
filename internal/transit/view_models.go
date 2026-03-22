package transit

import (
	"thundercitizen/internal/transit/chunk"
)

// StopAlert is alert info for a specific stop, passed to the map JS.
type StopAlert struct {
	Header      string `json:"header"`
	Description string `json:"description"`
}

// LiveViewModel contains data for the live map page (/transit).
type LiveViewModel struct {
	Alerts          []ActiveAlert              // route-level alerts (shown at top)
	CancelledRoutes []string                   // route IDs with active cancellations
	CancelledTrips  map[string][]CancelledTrip // route ID → cancelled trip details
	CancelIncidents []CancelIncident           // consecutive cancellations grouped
	StopAlerts      map[string][]StopAlert     // stop ID → alerts (for map markers)
	FleetSize       int                        // total unique vehicles ever seen
	NoServiceRoutes []string                   // route IDs with no service today
	RouteMeta       []RouteMetaAPI             // colors, names, terminals for JS
}

// DateRange holds the computed week range for the date range browser nav.
type DateRange struct {
	From     string // YYYY-MM-DD
	To       string // YYYY-MM-DD
	Label    string // "Mon Mar 24 – Sun Mar 30"
	PrevURL  string // "?end=2026-03-24" or "" if at start
	NextURL  string // "?end=2026-04-07" or "" if at end
	IsLatest bool   // true when showing the default trailing-7-day range
}

// MetricsViewModel contains data for the metrics page (/transit/metrics).
//
// Chunks is the single source of truth for metrics — server templates and
// the embedded JS module both read it. The page embeds it via
// @templ.JSONScript so the frontend has the data on first paint without a
// fetch. CancelledTrips is a separate per-trip log embedded the same way
// for the cancel-card drill-down.
type MetricsViewModel struct {
	KPI            string         // active KPI key (otp, cancel, notice, wait, ewt, cv)
	RouteMeta      []RouteMetaAPI // needed for route comparison chart
	Range          DateRange
	Chunks         []chunk.ChunkView // 7 days × 3 bands × N routes — THE metrics shape
	CancelledTrips []CancelDetail    // per-trip cancel log for the date range
	HasData        bool
}

// RoutesViewModel contains data for the routes directory page (/transit/routes).
type RoutesViewModel struct {
	RouteMeta []RouteMetaAPI
	Range     DateRange
	Chunks    []chunk.ChunkView // same shape as MetricsViewModel.Chunks
}

// MethodViewModel holds data collection intervals for the methodology page.
type MethodViewModel struct {
	VehiclePoll string
	TripPoll    string
	AlertPoll   string
}

// NewLiveViewModel creates the view model for the live map page.
func NewLiveViewModel(alerts []ActiveAlert, cancelledTrips map[string][]CancelledTrip) LiveViewModel {
	// Build cancelled routes list from trip details + alert-affected routes
	routeSet := make(map[string]bool)
	for r := range cancelledTrips {
		routeSet[r] = true
	}
	for _, a := range alerts {
		for _, r := range a.AffectedRoutes {
			routeSet[r] = true
		}
	}
	merged := make([]string, 0, len(routeSet))
	for r := range routeSet {
		merged = append(merged, r)
	}

	// Build stop alerts map and filter route-level alerts for top display
	stopAlerts := make(map[string][]StopAlert)
	var routeAlerts []ActiveAlert
	for _, a := range alerts {
		if len(a.AffectedStops) > 0 {
			sa := StopAlert{}
			if a.Header != nil {
				sa.Header = *a.Header
			}
			if a.Description != nil {
				sa.Description = *a.Description
			}
			seen := map[string]bool{}
			for _, stopID := range a.AffectedStops {
				if !seen[stopID] {
					seen[stopID] = true
					stopAlerts[stopID] = append(stopAlerts[stopID], sa)
				}
			}
		}
		// Only show route-level alerts without specific stops in the banner.
		// Stop-specific alerts (detours, closures) are shown in the stop popup instead.
		if len(a.AffectedRoutes) > 0 && len(a.AffectedStops) == 0 {
			routeAlerts = append(routeAlerts, a)
		}
	}

	return LiveViewModel{
		Alerts:          routeAlerts,
		CancelledRoutes: merged,
		CancelledTrips:  cancelledTrips,
		StopAlerts:      stopAlerts,
	}
}

// RouteViewModel contains data for the per-route detail page.
type RouteViewModel struct {
	RouteID            string
	ShortName          string
	LongName           string
	Color              string
	TextColor          string
	Date               string
	DateISO            string // YYYY-MM-DD for day picker links
	IsToday            bool   // true when viewing today's schedule (show actuals)
	Trips              []ScheduledTrip
	Alerts             []ActiveAlert
	TimepointSchedules []TimepointSchedule
	Unified            *UnifiedSchedule
	ServiceDays        map[string]bool // ISO dates with service this week
	CancelDays         map[string]int  // ISO date → cancellation count
	CancelledTrips     []CancelledTrip // cancelled trips for this route today
	TotalTrips         int             // total trip observations since tracking began
	TrackingSince      string          // first observation date (e.g. "Mar 20, 2026")
}
