package transit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/cache"
	"thundercitizen/internal/httperr"
)

// Renderer renders page templates. Injected to avoid import cycles
// between the transit package and templates/pages.
type Renderer struct {
	TransitLive               func(vm LiveViewModel) RenderFunc
	TransitMetrics            func(vm MetricsViewModel) RenderFunc
	TransitRoutes             func(vm RoutesViewModel) RenderFunc
	TransitMethod             func(vm MethodViewModel) RenderFunc
	Route                     func(vm RouteViewModel) RenderFunc
	RoutePartial              func(vm RouteViewModel) RenderFunc
	RouteSchedulePartial      func(vm RouteViewModel) RenderFunc
	RouteScheduleTodayPartial func(vm RouteViewModel) RenderFunc
	RouteScheduleBodyPartial  func(vm RouteViewModel) RenderFunc
	AuditIndex                func(vm AuditIndexViewModel) RenderFunc
	AuditRoute                func(vm AuditRouteViewModel) RenderFunc
	PlanPartial               func(plan *PlanResult, summary bool, fromLat, fromLon, toLat, toLon float64) RenderFunc
}

// RenderFunc writes rendered HTML to the writer.
type RenderFunc func(ctx context.Context, w io.Writer) error

// Handler serves all transit page and API routes.
// It is a thin HTTP adapter — business logic and caching live in Service.
type Handler struct {
	svc           *Service
	render        Renderer
	VehicleStream *VehicleStream
}

// NewHandler creates a transit handler backed by a Service.
func NewHandler(db *pgxpool.Pool, render Renderer) *Handler {
	svc := NewService(db)
	return &Handler{
		svc:           svc,
		render:        render,
		VehicleStream: svc.stream,
	}
}

// PageRoutes returns a chi.Router with transit page routes.
// Mount at /transit.
func (h *Handler) PageRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.transitLivePage)
	r.Get("/metrics", h.transitMetricsPage)
	r.Get("/routes", h.transitRoutesPage)
	r.Get("/method", h.transitMethodPage)
	r.Get("/report", h.transitReport)
	r.Get("/route/{id}", h.routePage)
	r.Get("/audit/deltas", h.auditIndex)
	r.Get("/audit/deltas/{id}", h.auditRoute)
	return r
}

// APIRoutes returns a chi.Router with transit API routes.
// Mount at /api/transit.
func (h *Handler) APIRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/vehicles", h.vehicles)
	r.Get("/vehicles.json", h.vehiclesJSON)
	r.Get("/vehicles/stream", h.vehiclesSSE)
	r.Get("/stats", h.stats)
	r.Get("/stops/nearby", h.nearbyStops)
	r.Get("/stops", h.stops)
	r.Get("/stop/{id}/predictions", h.stopPredictions)
	r.Get("/plan", h.plan)
	r.Get("/vehicle/{vehicleID}/distance/{stopID}", h.vehicleDistance)
	r.Get("/stops/analytics", h.stopAnalytics)
	r.Get("/timepoints", h.timepoints)
	r.Get("/routes", h.routesMeta)
	return r
}

// parseDateRange builds a DateRange from the "end" query param.
// No param = 7-day trailing from today. With param = 7 days ending on that date.
//
// The window is ALWAYS 7 days wide. If the underlying data only goes back
// 2 days, we still render a 7-day grid — the empty days show up as
// blank cells, which is more honest than collapsing the window. The
// PrevURL link below still gets disabled at the data boundary so users
// can't keep clicking into the void, but the visible shape stays
// consistent at 7 × 3 cells regardless of how much history we have.
func parseDateRange(r *http.Request, sinceDate string) DateRange {
	today := Today()

	// Default: trailing 7 days ending today
	to := today

	if s := r.URL.Query().Get("end"); s != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", s, TZ); err == nil {
			to = parsed
			if to.After(today) {
				to = today
			}
		}
	}

	from := to.AddDate(0, 0, -6)

	// Parse the data-availability boundary for the prev/next arrow
	// disable logic, but DO NOT clamp `from` to it — the window stays
	// 7 days wide even when we don't have data that far back.
	var since time.Time
	if sinceDate != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", sinceDate, TZ); err == nil {
			since = parsed
		}
	}

	// Format label: "Mon Mar 24 – Sun Mar 30"
	days := [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	mos := [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	fmtD := func(t time.Time) string {
		return fmt.Sprintf("%s %s %d", days[t.Weekday()], mos[t.Month()-1], t.Day())
	}

	basePath := r.URL.Path

	dr := DateRange{
		From:  from.Format("2006-01-02"),
		To:    to.Format("2006-01-02"),
		Label: fmtD(from) + " – " + fmtD(to),
	}

	dr.IsLatest = !to.Before(today)

	// Prev: 7 days earlier
	prevTo := to.AddDate(0, 0, -7)
	if !since.IsZero() && prevTo.Before(since) {
		dr.PrevURL = ""
	} else {
		dr.PrevURL = basePath + "?end=" + prevTo.Format("2006-01-02")
	}

	// Next: 7 days later (no param if it lands on today)
	atEnd := !to.Before(today)
	if !atEnd {
		nextTo := to.AddDate(0, 0, 7)
		if !nextTo.Before(today) {
			dr.NextURL = basePath // no param = latest
		} else {
			dr.NextURL = basePath + "?end=" + nextTo.Format("2006-01-02")
		}
	}

	return dr
}

// --- Page handlers ---

func (h *Handler) transitLivePage(w http.ResponseWriter, r *http.Request) {
	live := h.svc.Live()
	if live == nil || live.dashboard == nil {
		httperr.Unavailable(w, "live data cache warming")
		return
	}

	vm := NewLiveViewModel(live.dashboard.Alerts, live.dashboard.CancelledTrips)
	vm.FleetSize = live.dashboard.FleetSize
	vm.CancelIncidents = live.incidents
	vm.NoServiceRoutes = live.noService
	vm.RouteMeta = h.svc.RouteMeta()

	h.render.TransitLive(vm)(r.Context(), w)
}

func (h *Handler) transitMetricsPage(w http.ResponseWriter, r *http.Request) {
	var vm MetricsViewModel
	vm.KPI = "otp"
	vm.RouteMeta = h.svc.RouteMeta()
	vm.Range = parseDateRange(r, h.svc.SinceDate(r.Context()))

	if from, err := time.ParseInLocation("2006-01-02", vm.Range.From, TZ); err == nil {
		if to, err := time.ParseInLocation("2006-01-02", vm.Range.To, TZ); err == nil {
			if chunks, err := h.svc.Chunks(r.Context(), from, to); err == nil {
				vm.Chunks = chunks
				vm.HasData = len(chunks) > 0
			}
			if cancels, err := LoadCancelDetails(r.Context(), h.svc.db, from, to); err == nil {
				vm.CancelledTrips = cancels
			}
		}
	}

	h.render.TransitMetrics(vm)(r.Context(), w)
}

func (h *Handler) transitRoutesPage(w http.ResponseWriter, r *http.Request) {
	var vm RoutesViewModel
	vm.RouteMeta = h.svc.RouteMeta()
	vm.Range = parseDateRange(r, h.svc.SinceDate(r.Context()))
	if from, err := time.ParseInLocation("2006-01-02", vm.Range.From, TZ); err == nil {
		if to, err := time.ParseInLocation("2006-01-02", vm.Range.To, TZ); err == nil {
			if chunks, err := h.svc.Chunks(r.Context(), from, to); err == nil {
				vm.Chunks = chunks
			}
		}
	}
	h.render.TransitRoutes(vm)(r.Context(), w)
}

func (h *Handler) transitMethodPage(w http.ResponseWriter, r *http.Request) {
	h.render.TransitMethod(MethodViewModel{
		VehiclePoll: VehiclePollInterval.String(),
		TripPoll:    TripPollInterval.String(),
		AlertPoll:   AlertPollInterval.String(),
	})(r.Context(), w)
}

func (h *Handler) transitReport(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/transit", http.StatusMovedPermanently)
}

func (h *Handler) routePage(w http.ResponseWriter, r *http.Request) {
	routeID := chi.URLParam(r, "id")
	if routeID == "" {
		http.NotFound(w, r)
		return
	}

	// Optional date parameter for schedule views.
	var schedDate time.Time
	if ds := r.URL.Query().Get("date"); ds != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", ds, TZ); err == nil {
			schedDate = parsed
		}
	}

	partial := r.URL.Query().Get("partial")

	// Full page: dedicated route page with schedule + stats.
	if partial == "" {
		date := ServiceDate()
		if !schedDate.IsZero() {
			date = schedDate
		}
		info, err := h.svc.RouteInfo(r.Context(), routeID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tp, err := h.svc.RouteTimepointSchedule(r.Context(), routeID, date)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		totalTrips, since := h.svc.RouteTrackingStats(r.Context(), routeID)
		var sinceLabel string
		if since != "" {
			if t, err := time.ParseInLocation("2006-01-02", since, TZ); err == nil {
				sinceLabel = t.Format("Jan 2, 2006")
			}
		}
		vm := RouteViewModel{
			RouteID:       info.RouteID,
			ShortName:     info.ShortName,
			LongName:      info.LongName,
			Color:         info.Color,
			TextColor:     info.TextColor,
			Date:          date.Format("Monday, January 2"),
			DateISO:       date.Format("2006-01-02"),
			IsToday:       date.Format("2006-01-02") == ServiceDate().Format("2006-01-02"),
			Unified:       UnifySchedules(tp),
			TotalTrips:    totalTrips,
			TrackingSince: sinceLabel,
		}
		vm.ServiceDays = h.svc.RouteServiceDays(r.Context(), routeID, date)
		vm.CancelDays = h.svc.RouteCancelDays(r.Context(), routeID, date)
		if allCancels, err := CancelledTripDetails(r.Context(), h.svc.db, date); err == nil {
			vm.CancelledTrips = allCancels[routeID]
		}
		h.render.Route(vm)(r.Context(), w)
		return
	}

	// Fast path: schedule-body only needs schedule + timetable, skip metrics
	if partial == "schedule-body" {
		date := ServiceDate()
		if !schedDate.IsZero() {
			date = schedDate
		}
		info, err := h.svc.RouteInfo(r.Context(), routeID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tp, err := h.svc.RouteTimepointSchedule(r.Context(), routeID, date)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		vm := RouteViewModel{
			RouteID: info.RouteID,
			IsToday: date.Format("2006-01-02") == ServiceDate().Format("2006-01-02"),
			Unified: UnifySchedules(tp),
		}
		h.render.RouteScheduleBodyPartial(vm)(r.Context(), w)
		return
	}

	// Fast path for schedule partials — only need schedule + timetable, skip metrics.
	if partial == "1" || partial == "schedule" || partial == "schedule-today" {
		date := ServiceDate()
		if partial != "schedule-today" && !schedDate.IsZero() {
			date = schedDate
		}
		info, err := h.svc.RouteInfo(r.Context(), routeID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tp, err := h.svc.RouteTimepointSchedule(r.Context(), routeID, date)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		vm := RouteViewModel{
			RouteID:   info.RouteID,
			ShortName: info.ShortName,
			LongName:  info.LongName,
			Color:     info.Color,
			TextColor: info.TextColor,
			DateISO:   date.Format("2006-01-02"),
			IsToday:   date.Format("2006-01-02") == ServiceDate().Format("2006-01-02"),
			Unified:   UnifySchedules(tp),
		}
		if partial == "schedule-today" {
			h.render.RouteScheduleTodayPartial(vm)(r.Context(), w)
		} else {
			vm.ServiceDays = h.svc.RouteServiceDays(r.Context(), routeID, date)
			vm.CancelDays = h.svc.RouteCancelDays(r.Context(), routeID, date)
			if partial == "1" {
				if allCancels, err := CancelledTripDetails(r.Context(), h.svc.db, date); err == nil {
					vm.CancelledTrips = allCancels[routeID]
				}
			}
			if partial == "schedule" {
				h.render.RouteSchedulePartial(vm)(r.Context(), w)
			} else {
				h.render.RoutePartial(vm)(r.Context(), w)
			}
		}
		return
	}

	http.NotFound(w, r)
}

// --- API handlers ---

func (h *Handler) vehicles(w http.ResponseWriter, r *http.Request) {
	raw := h.VehicleStream.RawFeed()
	if raw == nil {
		httperr.Unavailable(w, "vehicle feed not available")
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Cache-Control", cache.Live)
	w.Write(raw)
}

func (h *Handler) vehiclesSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable the server-wide 15s WriteTimeout for this long-lived stream.
	// Without this, every SSE connection would be force-closed ~15s after
	// it opens, producing NS_ERROR_NET_* in Firefox and forcing the browser
	// to reconnect every keepalive tick. Scoped per-connection — other
	// routes retain the global 15s WriteTimeout protection.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		streamLog.Warn("sse: could not clear write deadline", "err", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", cache.Live)
	w.Header().Set("Connection", "keep-alive")

	// Send current state immediately
	if cur := h.VehicleStream.Current(); cur != nil {
		fmt.Fprintf(w, "data: %s\n\n", cur)
		flusher.Flush()
	}

	// Subscribe to updates
	ch := h.VehicleStream.Subscribe()
	defer h.VehicleStream.Unsubscribe(ch)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case payload, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handler) vehiclesJSON(w http.ResponseWriter, r *http.Request) {
	cur := h.VehicleStream.Current()
	if cur == nil {
		httperr.Unavailable(w, "vehicle feed not available")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cache.Live)
	w.Write(cur)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	variant := r.URL.Query().Get("range")
	if variant == "" {
		variant = "day"
	}

	report := h.svc.Stats(variant)
	if report == nil {
		httperr.Unavailable(w, "stats unavailable")
		return
	}
	writeJSON(w, cache.Short, report)
}

func (h *Handler) stops(w http.ResponseWriter, r *http.Request) {
	allStops := h.svc.AllStops()
	if allStops == nil {
		httperr.Unavailable(w, "stops unavailable")
		return
	}
	// Stop inventory changes only when GTFS reloads.
	writeJSON(w, cache.Reference, allStops)
}

func (h *Handler) stopAnalytics(w http.ResponseWriter, r *http.Request) {
	results := h.svc.StopAnalytics()
	if results == nil {
		httperr.Unavailable(w, "stop analytics unavailable")
		return
	}
	writeJSON(w, cache.Page, results)
}

func (h *Handler) routesMeta(w http.ResponseWriter, r *http.Request) {
	routes := h.svc.RouteMeta()
	if routes == nil {
		httperr.Unavailable(w, "route meta unavailable")
		return
	}
	// Route metadata only changes when GTFS reloads (rare), so tell the
	// browser it can cache this for an hour without revalidating.
	writeJSON(w, cache.Reference, routes)
}

func (h *Handler) timepoints(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.TimepointStops(r.Context())
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	writeJSON(w, cache.Reference, result)
}

func (h *Handler) stopPredictions(w http.ResponseWriter, r *http.Request) {
	stopID := chi.URLParam(r, "id")
	stopID = strings.TrimSuffix(stopID, "/predictions")
	stopID = strings.TrimRight(stopID, "/")
	if stopID == "" {
		httperr.BadRequest(w, "missing stop_id")
		return
	}

	predictions, err := h.svc.StopPredictions(r.Context(), stopID)
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	writeJSON(w, cache.Short, predictions)
}

// --- trip planner ---

func (h *Handler) plan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	fromLat, err := strconv.ParseFloat(q.Get("from_lat"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid from_lat")
		return
	}
	fromLon, err := strconv.ParseFloat(q.Get("from_lon"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid from_lon")
		return
	}
	toLat, err := strconv.ParseFloat(q.Get("to_lat"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid to_lat")
		return
	}
	toLon, err := strconv.ParseFloat(q.Get("to_lon"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid to_lon")
		return
	}

	now := Now()

	// Parse date, default to today
	date := now
	if d := q.Get("date"); d != "" {
		if parsed, err := time.Parse("2006-01-02", d); err == nil {
			date = parsed
		}
	}

	var result *PlanResult

	if arriveBy := q.Get("arrive_by"); arriveBy != "" {
		// Arrive-by mode: find latest departure that arrives on time
		var ah, am int
		if _, err := fmt.Sscanf(arriveBy, "%d:%d", &ah, &am); err != nil {
			httperr.BadRequest(w, "invalid arrive_by (use HH:MM)")
			return
		}
		result, err = h.svc.TripPlanArriveBy(r.Context(),
			LatLng{fromLat, fromLon}, LatLng{toLat, toLon},
			q.Get("from_stop"), q.Get("to_stop"),
			ah*3600+am*60, date)
	} else {
		// Depart-at mode (default)
		departSec := now.Hour()*3600 + now.Minute()*60
		if d := q.Get("depart"); d != "" {
			var dh, dm int
			if _, err := fmt.Sscanf(d, "%d:%d", &dh, &dm); err == nil {
				departSec = dh*3600 + dm*60
			}
		}
		result, err = h.svc.TripPlan(r.Context(),
			LatLng{fromLat, fromLon}, LatLng{toLat, toLon},
			q.Get("from_stop"), q.Get("to_stop"),
			departSec, date)
	}
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	if q.Get("partial") != "" {
		summary := q.Get("summary") == "1"
		h.render.PlanPartial(result, summary, fromLat, fromLon, toLat, toLon)(r.Context(), w)
		return
	}
	writeJSON(w, cache.Live, result)
}

// --- spatial handlers ---

func (h *Handler) nearbyStops(w http.ResponseWriter, r *http.Request) {
	lat, err := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid lat parameter")
		return
	}
	lon, err := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if err != nil {
		httperr.BadRequest(w, "invalid lon parameter")
		return
	}

	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	stops, err := h.svc.NearbyStops(r.Context(), lat, lon, limit)
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	writeJSON(w, cache.Reference, stops)
}

func (h *Handler) vehicleDistance(w http.ResponseWriter, r *http.Request) {
	vehicleID := chi.URLParam(r, "vehicleID")
	stopID := chi.URLParam(r, "stopID")
	if vehicleID == "" || stopID == "" {
		httperr.BadRequest(w, "missing vehicleID or stopID")
		return
	}

	dist, err := h.svc.VehicleDistance(r.Context(), vehicleID, stopID)
	if errors.Is(err, pgx.ErrNoRows) {
		httperr.NotFound(w, "vehicle or stop not found")
		return
	}
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	writeJSON(w, cache.Live, dist)
}

// --- helpers ---

func fmtDelay(sec float64) string {
	abs := sec
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if sec < 0 {
		sign = "-"
	}
	if abs < 60 {
		return fmt.Sprintf("%s%.0fs", sign, abs)
	}
	m := int(abs) / 60
	s := int(abs) % 60
	if s > 0 {
		return fmt.Sprintf("%s%dm%ds", sign, m, s)
	}
	return fmt.Sprintf("%s%dm", sign, m)
}

func writeJSON(w http.ResponseWriter, cacheControl string, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cacheControl)
	json.NewEncoder(w).Encode(v)
}

func parseDays(r *http.Request, defaultDays, maxDays int) int {
	days := defaultDays
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= maxDays {
			days = parsed
		}
	}
	return days
}
