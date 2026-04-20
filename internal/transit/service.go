package transit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/transit/chunk"
)

// Service owns transit business state: the ChunkCache for metric data,
// the auxiliary RepoCache for everything else, the reporter, and the
// vehicle stream. Handlers delegate all data access here.
//
// Metrics flow through the ChunkCache. Everything else — live dashboard,
// route metadata, stats reports, stop analytics — flows through the
// legacy RepoCache. The ChunkCache is the only thing that talks to
// transit.route_band_chunk; everything in RepoCache reads other tables.
//
// ChunkCache is exported so handlers can call it directly:
//
//	chunks,    err := h.svc.ChunkCache.Range(ctx, from, to)
//	one,  ok,  err := h.svc.ChunkCache.One(ctx, "3", today, "morning")
//	earliest      := h.svc.ChunkCache.EarliestDate()
type Service struct {
	db         *pgxpool.Pool
	reporter   *Reporter
	stream     *VehicleStream
	recorder   *Recorder
	cache      *RepoCache
	ChunkCache *ChunkCache

	// Testability hooks — override in tests to avoid hitting the DB.
	getRoute             func(ctx context.Context, routeID string) (*RouteInfo, error)
	routeTimepointLoader func(ctx context.Context, routeID string, date time.Time) ([]TimepointSchedule, error)
}

// liveData bundles all data for the transit live page.
type liveData struct {
	dashboard *DashboardReport
	incidents []CancelIncident
	noService []string
}

// NewService creates a transit service with its dependencies. Pass the
// recorder so stop predictions can serve from its in-memory trip feed
// instead of re-fetching from the upstream API.
func NewService(db *pgxpool.Pool, recorder *Recorder) *Service {
	client := NewClient()
	reporter := NewReporter(db, client)
	return &Service{
		db:         db,
		reporter:   reporter,
		stream:     NewVehicleStream(client, db, 6*time.Second),
		recorder:   recorder,
		cache:      NewRepoCache(reporter),
		ChunkCache: NewChunkCache(db),
	}
}

// ---------------------------------------------------------------------------
// Cache readers — every accessor lazy-loads on miss via RepoCache.
//
// There are no background warmers. The first caller after boot pays the
// compute cost; every subsequent caller hits the cache. The `live` slot
// has a TTL (30s) so dashboard data stays fresh; every other slot caches
// forever until the key changes (e.g. date range rolls at midnight).
//
// All accessors return nil/zero on loader error — handlers map that to a
// 503 or an empty render as they see fit.
// ---------------------------------------------------------------------------

func newCacheCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// RouteMeta returns route metadata (lazy-loaded on first call).
func (s *Service) RouteMeta() []RouteMetaAPI {
	ctx, cancel := newCacheCtx()
	defer cancel()
	v, err := s.cache.routeMeta.Get(ctx)
	if err != nil {
		return nil
	}
	return v
}

// SinceDate returns the earliest date the database has chunks for,
// formatted as YYYY-MM-DD, or "" if the table is empty. Used by the
// date selector to disable the prev arrow at the data boundary.
func (s *Service) SinceDate(ctx context.Context) string {
	d := s.ChunkCache.EarliestDate(ctx)
	if d.IsZero() {
		return ""
	}
	return d.Format("2006-01-02")
}

// Chunks returns the chunks in [from, to] inclusive. Thin wrapper over
// ChunkCache.Range — handlers can call either, this exists so existing
// call sites that read like "give me the metrics" stay readable.
func (s *Service) Chunks(ctx context.Context, from, to time.Time) ([]chunk.ChunkView, error) {
	return s.ChunkCache.Range(ctx, from, to)
}

// Stats returns a stats report by variant ("day", "week", "percentiles").
func (s *Service) Stats(variant string) *StatsReport {
	ctx, cancel := newCacheCtx()
	defer cancel()
	report, err := s.cache.stats.Get(ctx, variant)
	if err != nil {
		return nil
	}
	return report
}

// Live returns the live dashboard bundle. Slot has a 30s TTL so repeated
// calls within that window hit the cache; a call after expiry re-loads.
func (s *Service) Live() *liveData {
	ctx, cancel := newCacheCtx()
	defer cancel()
	v, err := s.cache.live.Get(ctx)
	if err != nil {
		return nil
	}
	return v
}

// AllStops returns all stops (lazy-loaded on first call).
func (s *Service) AllStops() []Stop {
	ctx, cancel := newCacheCtx()
	defer cancel()
	v, err := s.cache.allStops.Get(ctx)
	if err != nil {
		return nil
	}
	return v
}

// StopAnalytics returns per-stop analytics rows (lazy-loaded on first call).
func (s *Service) StopAnalytics() []StopAnalyticsRow {
	ctx, cancel := newCacheCtx()
	defer cancel()
	v, err := s.cache.stopAnalytics.Get(ctx)
	if err != nil {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// Data access helpers
// ---------------------------------------------------------------------------

// RouteInfo looks up a route, using the testability hook if set.
func (s *Service) RouteInfo(ctx context.Context, routeID string) (*RouteInfo, error) {
	if s.getRoute != nil {
		return s.getRoute(ctx, routeID)
	}
	return s.reporter.repo.GetRoute(ctx, routeID)
}

// RouteTimepointSchedule loads timepoint schedule data, using the hook if set.
func (s *Service) RouteTimepointSchedule(ctx context.Context, routeID string, date time.Time) ([]TimepointSchedule, error) {
	if s.routeTimepointLoader != nil {
		return s.routeTimepointLoader(ctx, routeID, date)
	}
	return RouteTimepointSchedule(ctx, s.reporter.db, routeID, date)
}

// RouteServiceDays returns which days in the week of refDate had service for
// routeID. "Had service" means we saw evidence the route was in operation that
// day — either we recorded a stop delay (at least one trip ran) or we recorded
// a cancellation (at least one trip was scheduled, even if it didn't run).
//
// Deliberately does NOT consult transit_calendar_dates: a long-lived prod DB
// can drift past the GTFS bundle's calendar coverage and suddenly every day
// looks like "no service." Observed data is the authority for past and
// current days. Future days never need this function because the week picker
// template disables them via the d.IsFuture check regardless.
func (s *Service) RouteServiceDays(ctx context.Context, routeID string, refDate time.Time) map[string]bool {
	offset := int(refDate.Weekday()) - 1
	if offset < 0 {
		offset = 6
	}
	monday := refDate.AddDate(0, 0, -offset)
	sunday := monday.AddDate(0, 0, 6)

	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT day FROM (
			SELECT date::TEXT AS day
			FROM transit.stop_delay
			WHERE route_id = $1 AND date BETWEEN $2::date AND $3::date
			UNION
			SELECT TO_CHAR(TO_DATE(start_date, 'YYYYMMDD'), 'YYYY-MM-DD') AS day
			FROM transit.cancellation
			WHERE route_id = $1 AND start_date IS NOT NULL
			  AND start_date BETWEEN TO_CHAR($2::date, 'YYYYMMDD')
			                     AND TO_CHAR($3::date, 'YYYYMMDD')
		) sub
	`, routeID, monday, sunday)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var iso string
		if err := rows.Scan(&iso); err == nil {
			result[iso] = true
		}
	}
	return result
}

// RouteCancelDays returns cancellation counts per day in the week of refDate for routeID.
func (s *Service) RouteCancelDays(ctx context.Context, routeID string, refDate time.Time) map[string]int {
	offset := int(refDate.Weekday()) - 1
	if offset < 0 {
		offset = 6
	}
	monday := refDate.AddDate(0, 0, -offset)
	sunday := monday.AddDate(0, 0, 6)

	rows, err := s.db.Query(ctx, `
		SELECT start_date, COUNT(DISTINCT trip_id)
		FROM transit.cancellation
		WHERE route_id = $1
			AND start_date >= $2 AND start_date <= $3
		GROUP BY start_date
	`, routeID, monday.Format("20060102"), sunday.Format("20060102"))
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var sd string
		var count int
		if err := rows.Scan(&sd, &count); err == nil && len(sd) == 8 {
			result[sd[:4]+"-"+sd[4:6]+"-"+sd[6:8]] = count
		}
	}
	return result
}

// RouteTrackingStats returns total trip observations and first observation date for a route.
func (s *Service) RouteTrackingStats(ctx context.Context, routeID string) (totalTrips int, since string) {
	s.reporter.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT (trip_id, date))::INT, COALESCE(MIN(date)::TEXT, '')
		FROM transit.stop_delay WHERE route_id = $1
	`, routeID).Scan(&totalTrips, &since)
	return
}

// ---------------------------------------------------------------------------
// Reporter delegation
// ---------------------------------------------------------------------------

// StopPredictions returns arrival predictions for a stop. Prefers the
// recorder's in-memory trip feed (refreshed every ~60s by the trips
// poller) so the hot path avoids a synchronous upstream HTTP fetch.
// Falls back to a direct client fetch only on cold boot, before the
// recorder has completed its first poll.
func (s *Service) StopPredictions(ctx context.Context, stopID string) (StopPredictionsResponse, error) {
	feed := s.recorder.LastTripFeed()
	if feed == nil {
		return s.reporter.StopPredictionsReport(ctx, stopID)
	}
	resp, err := StopPredictionsFromFeed(ctx, s.reporter.db, feed, stopID, Now())
	if err != nil {
		return StopPredictionsResponse{}, err
	}
	if resp.Predictions == nil {
		resp.Predictions = []StopPrediction{}
	}
	return resp, nil
}

// TripPlan runs a depart-at trip plan.
func (s *Service) TripPlan(ctx context.Context, origin, dest LatLng, fromStop, toStop string, departSec int, date time.Time) (*PlanResult, error) {
	return s.reporter.TripPlan(ctx, origin, dest, fromStop, toStop, departSec, date)
}

// TripPlanArriveBy runs an arrive-by trip plan.
func (s *Service) TripPlanArriveBy(ctx context.Context, origin, dest LatLng, fromStop, toStop string, arriveSec int, date time.Time) (*PlanResult, error) {
	return s.reporter.TripPlanArriveBy(ctx, origin, dest, fromStop, toStop, arriveSec, date)
}

// NearbyStops returns stops near a lat/lon.
func (s *Service) NearbyStops(ctx context.Context, lat, lon float64, limit int) ([]StopWithDistance, error) {
	return s.reporter.NearestStopsReport(ctx, lat, lon, limit)
}

// VehicleDistance returns the distance of a vehicle from a stop.
func (s *Service) VehicleDistance(ctx context.Context, vehicleID, stopID string) (*VehicleDistance, error) {
	return s.reporter.VehicleDistanceReport(ctx, vehicleID, stopID)
}

// MapTimepointStop is one entry in the timepoints-for-map API response.
type MapTimepointStop struct {
	StopID string   `json:"stop_id"`
	Routes []string `json:"routes"`
	Colors []string `json:"colors"`
}

// TimepointStops returns timepoint stops grouped by stop_id with route colors for map rendering.
func (s *Service) TimepointStops(ctx context.Context) ([]MapTimepointStop, error) {
	tps, err := s.reporter.repo.AllRouteTimepoints(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.reporter.repo.RouteDisplayInfo(ctx)
	if err != nil {
		return nil, err
	}

	colorMap := map[string]string{}
	for _, rd := range routes {
		if rd.Color != "" {
			colorMap[rd.RouteID] = rd.Color
		}
	}

	stopRoutes := map[string]map[string]bool{}
	for routeID, rtp := range tps {
		for _, tp := range rtp {
			if stopRoutes[tp.StopID] == nil {
				stopRoutes[tp.StopID] = map[string]bool{}
			}
			stopRoutes[tp.StopID][routeID] = true
		}
	}

	var result []MapTimepointStop
	for stopID, routeSet := range stopRoutes {
		ts := MapTimepointStop{StopID: stopID}
		for rid := range routeSet {
			ts.Routes = append(ts.Routes, rid)
			ts.Colors = append(ts.Colors, colorMap[rid])
		}
		result = append(result, ts)
	}
	return result, nil
}
