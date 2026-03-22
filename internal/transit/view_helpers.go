package transit

import (
	"fmt"

	"thundercitizen/internal/transit/chunk"
)

// View helpers — pure functions that aggregate slices of chunk.ChunkView
// into the formatted strings the templates render. The aggregation itself
// lives in chunk.KPI (internal/transit/chunk/rollup.go); this file is the
// formatting layer on top of that single source of truth. The JS frontend
// mirrors chunk.KPI verbatim in static/transit/chunks.js so server- and
// client-side always agree on one number.

// formatKPI turns a chunk.KPI output into the string the template renders.
// Formatting rules come from the metric's own conventions: OTP/Notice are
// integer percents, Cancel/Wait/EWT have one decimal, Cv has two.
func formatKPI(metric chunk.Metric, v float64, ok bool) string {
	if !ok {
		return "—"
	}
	switch metric {
	case chunk.MetricOTP, chunk.MetricNotice:
		return fmt.Sprintf("%.0f", v)
	case chunk.MetricCancel, chunk.MetricWait, chunk.MetricEWT:
		return fmt.Sprintf("%.1f", v)
	case chunk.MetricCv:
		return fmt.Sprintf("%.2f", v)
	}
	return "—"
}

// KPIFromChunks returns a formatted string for one cell of the metrics
// tab — a metric ("otp"|"cancel"|"notice"|"wait"|"ewt"|"cv") optionally
// restricted to one band ("morning"|"midday"|"evening"; "" pools all
// three). Thin delegator over chunk.KPI. Exported because templates in
// package pages call it directly.
func KPIFromChunks(chunks []chunk.ChunkView, metric, band string) string {
	m := chunk.Metric(metric)
	v, ok := chunk.KPI(chunks, m, band)
	return formatKPI(m, v, ok)
}

// RouteRowKPI is the per-route summary the routes table renders.
type RouteRowKPI struct {
	OTP       string
	Cancel    string // cancelled trip count
	CancelPct string
	Trips     string
	EWT       string
	Cv        string
	HasData   bool
}

// RouteRowKPIFromChunks summarizes one route's chunks into the per-row
// fields the routes table displays. Pre-filters the slice to the route,
// then delegates each metric to chunk.KPI — which, applied to a single
// route, reduces to that route's own pooled per-metric reading. Raw
// Trips/Cancelled counts are still totaled here because the table
// displays them as integers alongside the formatted percentages.
//
// Returns a zero-value RouteRowKPI with HasData=false when the route has
// no chunks in the slice. Exported because templates in package pages
// call it directly.
func RouteRowKPIFromChunks(chunks []chunk.ChunkView, routeID string) RouteRowKPI {
	var routeChunks []chunk.ChunkView
	var trips, cancelled int
	for i := range chunks {
		if chunks[i].RouteID != routeID {
			continue
		}
		routeChunks = append(routeChunks, chunks[i])
		trips += chunks[i].Trips
		cancelled += chunks[i].Cancelled
	}
	if len(routeChunks) == 0 {
		return RouteRowKPI{}
	}

	row := RouteRowKPI{
		HasData: true,
		Trips:   fmt.Sprintf("%d", trips),
		Cancel:  fmt.Sprintf("%d", cancelled),
	}
	row.OTP = KPIFromChunks(routeChunks, "otp", "")
	row.CancelPct = KPIFromChunks(routeChunks, "cancel", "")
	row.EWT = KPIFromChunks(routeChunks, "ewt", "")
	row.Cv = KPIFromChunks(routeChunks, "cv", "")
	return row
}
