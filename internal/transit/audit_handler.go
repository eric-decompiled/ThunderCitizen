package transit

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"thundercitizen/internal/cache"
	"thundercitizen/internal/httperr"
)

// auditIndex lists every route that has metadata, linking to the per-route
// audit view. Maintainer tool only — no styling polish.
func (h *Handler) auditIndex(w http.ResponseWriter, r *http.Request) {
	date := ServiceDate()
	if ds := r.URL.Query().Get("date"); ds != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", ds, TZ); err == nil {
			date = parsed
		}
	}

	vm := AuditIndexViewModel{
		Routes:  h.svc.RouteMeta(),
		DateISO: date.Format("2006-01-02"),
	}
	w.Header().Set("Cache-Control", cache.Live)
	if h.render.AuditIndex == nil {
		httperr.Internal(w, nil)
		return
	}
	h.render.AuditIndex(vm)(r.Context(), w)
}

// auditRoute renders the per-route audit timetable: the same grid the public
// route page shows, but with each "actual" cell stacking the TripUpdate
// "obs" delay on top of the GPS "gps" delay so divergences stand out.
func (h *Handler) auditRoute(w http.ResponseWriter, r *http.Request) {
	routeID := chi.URLParam(r, "id")
	if routeID == "" {
		http.NotFound(w, r)
		return
	}

	date := ServiceDate()
	if ds := r.URL.Query().Get("date"); ds != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", ds, TZ); err == nil {
			date = parsed
		}
	}

	info, err := h.svc.RouteInfo(r.Context(), routeID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sched, err := h.svc.AuditTimetable(r.Context(), routeID, date)
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	vm := AuditRouteViewModel{
		RouteID:   info.RouteID,
		ShortName: info.ShortName,
		LongName:  info.LongName,
		Color:     info.Color,
		TextColor: info.TextColor,
		Date:      date.Format("Monday, January 2"),
		DateISO:   date.Format("2006-01-02"),
		Schedule:  sched,
	}
	w.Header().Set("Cache-Control", cache.Live)
	if h.render.AuditRoute == nil {
		httperr.Internal(w, nil)
		return
	}
	h.render.AuditRoute(vm)(r.Context(), w)
}
