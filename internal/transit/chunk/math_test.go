package chunk

import (
	"math"
	"testing"
	"time"
)

// Textbook example used by TfL Service Performance Methodology and reproduced
// in Furth & Muller (2006), "Service Reliability and Hidden Waiting Time".
//
// Five observed headway gaps at one timepoint (minutes): 5, 7, 3, 10, 5.
// In seconds: 300, 420, 180, 600, 300.
//
//	sum(h)   = 1800
//	sum(h^2) = 90000 + 176400 + 32400 + 360000 + 90000 = 748800
//	mean h   = 360
//	variance = ((300-360)^2 + (420-360)^2 + (180-360)^2 + (600-360)^2 + (300-360)^2) / 5
//	         = (3600 + 3600 + 32400 + 57600 + 3600) / 5
//	         = 20160
//	stddev   ≈ 141.986
//	Cv       ≈ 0.39440
//	AWT      = sum(h^2) / (2*sum(h)) = 748800 / 3600 = 208 sec
//
// With a scheduled headway of 5 min (300 sec), SWT = 150 and EWT = 58 sec.
// With a scheduled headway equal to the observed mean (360 sec), SWT = 180
// and EWT = 28 sec.
//
// The point of this example: AWT (208) is materially larger than the simple
// mean / 2 (180). A regression that collapses the formula back to mean/2
// would pass a uniform-headway test but fail this one.
const (
	textbookHeadwayCount    = 5
	textbookSumH            = 1800.0
	textbookSumHSq          = 748800.0
	textbookSchedHeadwaySec = 300.0
)

func almostEqual(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %v, want %v ± %v", name, got, want, tol)
	}
}

func TestCv_Textbook(t *testing.T) {
	cv := Cv(textbookHeadwayCount, textbookSumH, textbookSumHSq)
	almostEqual(t, "Cv", cv, 0.39440, 0.0005)
}

func TestEWTSec_Textbook(t *testing.T) {
	ewt := EWTSec(textbookSumH, textbookSumHSq, textbookSchedHeadwaySec)
	// AWT = 208, SWT = 150, EWT = 58
	almostEqual(t, "EWT", ewt, 58.0, 0.5)
}

func TestEWTSec_TextbookMatchedSchedule(t *testing.T) {
	// Scheduled headway = observed mean → AWT - SWT = 208 - 180 = 28
	ewt := EWTSec(textbookSumH, textbookSumHSq, 360.0)
	almostEqual(t, "EWT", ewt, 28.0, 0.5)
}

func TestEWTSec_OverperformingClampsZero(t *testing.T) {
	// If buses run more regularly than scheduled, AWT < SWT and EWT clamps
	// to zero — riders don't get "credit" for shorter waits than promised.
	ewt := EWTSec(textbookSumH, textbookSumHSq, 1000.0)
	almostEqual(t, "EWT", ewt, 0.0, 0.0001)
}

func TestWaitMin_Textbook(t *testing.T) {
	wait := WaitMin(textbookHeadwayCount, textbookSumH)
	// 1800 / 5 / 60 = 6 minutes
	almostEqual(t, "Wait", wait, 6.0, 0.001)
}

// Pooling identity: two chunks with the same per-stop headways should reach
// the same Cv whether you treat them as one big sample of 10 headways or as
// the column-summed totals of two chunks of 5. This is the load-bearing
// property that lets ComputeSystem aggregate Cv across the date range by
// SUM-ing chunk columns.
func TestCv_PoolingIsExact(t *testing.T) {
	// Single chunk with 5 headways.
	single := Cv(textbookHeadwayCount, textbookSumH, textbookSumHSq)

	// Two chunks, each with the same 5 headways. Pooled they're 10 copies
	// of the same distribution — the variance and mean are identical, so Cv
	// should be unchanged.
	pooled := Cv(
		2*textbookHeadwayCount,
		2*textbookSumH,
		2*textbookSumHSq,
	)
	almostEqual(t, "pooled vs single Cv", pooled, single, 1e-12)
}

// Pooling identity for EWT: same logic. AWT = sum(h²)/(2·sum(h)) is invariant
// under doubling both numerator and denominator, and SWT depends only on the
// scheduled headway, so two identical chunks pool to the same EWT.
func TestEWTSec_PoolingIsExact(t *testing.T) {
	single := EWTSec(textbookSumH, textbookSumHSq, textbookSchedHeadwaySec)
	pooled := EWTSec(2*textbookSumH, 2*textbookSumHSq, textbookSchedHeadwaySec)
	almostEqual(t, "pooled vs single EWT", pooled, single, 1e-12)
}

func TestCv_NotEnoughData(t *testing.T) {
	if got := Cv(0, 0, 0); got != 0 {
		t.Errorf("empty chunk Cv: got %v, want 0", got)
	}
	if got := Cv(1, 300, 90000); got != 0 {
		t.Errorf("single-headway Cv: got %v, want 0", got)
	}
}

// --- Reaggregator end-to-end ---

// Hand-built chunk fixture covering two routes across two dates and one
// band. Used to verify KPI produces the right values on a known input.
// The chunk sums correspond to the textbook headway distribution above
// so the test inherits its provenance.
//
// Returned as []ChunkView because that's the shape the live code path
// operates on — KPI is the single aggregation entry point and consumes
// the JSON-serializable ChunkView.
func textbookChunks() []ChunkView {
	// Route A — perfect day 1. 10 trips, 9 on time, 0 cancelled.
	// Headways match the textbook example: 5 gaps summing to 1800.
	a1 := Chunk{
		RouteID: "A", Date: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Band: "morning", ServiceKind: "weekday",
		TripCount: 10, OnTimeCount: 9,
		ScheduledCount: 12, CancelledCount: 0, NoNoticeCount: 0,
		HeadwayCount: textbookHeadwayCount, HeadwaySumSec: textbookSumH, HeadwaySumSecSq: textbookSumHSq,
		SchedHeadwaySec: textbookSchedHeadwaySec,
	}
	// Route A — day 2, same headway shape (so pooling 2× is exact).
	a2 := Chunk{
		RouteID: "A", Date: time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		Band: "morning", ServiceKind: "weekday",
		TripCount: 10, OnTimeCount: 7,
		ScheduledCount: 12, CancelledCount: 1, NoNoticeCount: 1,
		HeadwayCount: textbookHeadwayCount, HeadwaySumSec: textbookSumH, HeadwaySumSecSq: textbookSumHSq,
		SchedHeadwaySec: textbookSchedHeadwaySec,
	}
	// Route B — sparse, only 1 headway gap. Cv skipped for this route
	// because Cv needs at least 2 observations.
	b1 := Chunk{
		RouteID: "B", Date: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Band: "morning", ServiceKind: "weekday",
		TripCount: 4, OnTimeCount: 3,
		ScheduledCount: 5, CancelledCount: 0, NoNoticeCount: 0,
		HeadwayCount: 1, HeadwaySumSec: 600, HeadwaySumSecSq: 360000,
		SchedHeadwaySec: 600,
	}
	return []ChunkView{a1.View(), a2.View(), b1.View()}
}

// TestKPI_TextbookFixture asserts every system-level metric on the
// morning-band textbook fixture. Covers all six metrics on one input
// so the pooling/averaging rules are spelled out in one place.
func TestKPI_TextbookFixture(t *testing.T) {
	chunks := textbookChunks()
	get := func(m Metric, band string) (float64, bool) { return KPI(chunks, m, band) }

	// OTP: (9 + 7 + 3) / (10 + 10 + 4) = 19/24 ≈ 79.166 — pooled sum.
	if v, ok := get(MetricOTP, "morning"); !ok {
		t.Error("OTP: no data")
	} else {
		almostEqual(t, "OTP", v, 19.0*100/24.0, 0.001)
	}

	// Cancel: (0 + 1 + 0) / (12 + 12 + 5) = 1/29 ≈ 3.448 — pooled sum.
	if v, ok := get(MetricCancel, "morning"); !ok {
		t.Error("Cancel: no data")
	} else {
		almostEqual(t, "Cancel", v, 100.0/29.0, 0.001)
	}

	// Notice: 1/1 = 100 — pooled sum.
	if v, ok := get(MetricNotice, "morning"); !ok {
		t.Error("Notice: no data")
	} else {
		almostEqual(t, "Notice", v, 100.0, 0.001)
	}

	// Cv: per-route pool, then mean across routes.
	//   Route A has 10 pooled headways (textbook × 2) → Cv ≈ 0.394.
	//   Route B has only 1 headway → skipped.
	//   System Cv = mean of {0.394} = 0.394.
	if v, ok := get(MetricCv, "morning"); !ok {
		t.Error("Cv: no data")
	} else {
		almostEqual(t, "Cv", v, 0.39440, 0.0005)
	}

	// Wait: pooled mean gap in minutes. (1800 + 1800 + 600) / (5 + 5 + 1) = 4200/11 sec ≈ 6.36 min.
	if v, ok := get(MetricWait, "morning"); !ok {
		t.Error("Wait: no data")
	} else {
		almostEqual(t, "Wait", v, (4200.0/11.0)/60.0, 0.001)
	}

	// EWT: per-route pool then mean across routes.
	//   Route A pooled: sumH=3600, sumHSq=1497600, sched=300.
	//     AWT = 1497600/(2*3600) = 208 sec, SWT = 150 → EWT 58 sec ≈ 0.9667 min.
	//   Route B: sumH=600, sumHSq=360000, sched=600.
	//     AWT = 360000/1200 = 300, SWT = 300 → EWT clamps to 0.
	//   System EWT = mean({0.9667, 0}) ≈ 0.4833 min.
	if v, ok := get(MetricEWT, "morning"); !ok {
		t.Error("EWT: no data")
	} else {
		almostEqual(t, "EWT", v, (58.0/60.0+0.0)/2.0, 0.005)
	}

	// Empty data short-circuit: ok=false when the fixture has zero chunks.
	if _, ok := KPI(nil, MetricOTP, ""); ok {
		t.Error("empty slice should report ok=false")
	}

	// Other bands have no data in this fixture → ok=false across the board.
	for _, m := range []Metric{MetricOTP, MetricCancel, MetricNotice, MetricWait, MetricEWT, MetricCv} {
		if _, ok := KPI(chunks, m, "midday"); ok {
			t.Errorf("%s midday: expected ok=false, got true", m)
		}
	}
}

// TestKPI_PerRouteSliceEqualsRouteMetrics: pre-filtering to a single
// route and calling KPI should produce that route's own pooled metrics.
// This is the property RouteRowKPIFromChunks in the transit package
// relies on — per-row KPIs come from KPI applied to a single-route
// slice.
func TestKPI_PerRouteSliceEqualsRouteMetrics(t *testing.T) {
	chunks := textbookChunks()
	var routeA []ChunkView
	for _, c := range chunks {
		if c.RouteID == "A" {
			routeA = append(routeA, c)
		}
	}

	// OTP: (9+7)/(10+10) = 80%.
	if v, ok := KPI(routeA, MetricOTP, "morning"); !ok || math.Abs(v-80.0) > 0.001 {
		t.Errorf("route A OTP: got %v ok=%v, want 80", v, ok)
	}
	// Cv: pooled headways identical to textbook × 2 → unchanged.
	if v, ok := KPI(routeA, MetricCv, "morning"); !ok || math.Abs(v-0.39440) > 0.0005 {
		t.Errorf("route A Cv: got %v ok=%v, want ~0.394", v, ok)
	}
	// EWT: 58 sec / 60 ≈ 0.9667 min.
	if v, ok := KPI(routeA, MetricEWT, "morning"); !ok || math.Abs(v-(58.0/60.0)) > 0.005 {
		t.Errorf("route A EWT: got %v ok=%v, want ~0.9667", v, ok)
	}
}

// TestChunk_View round-trips a hand-built Chunk through .View() and
// asserts every field matches the textbook example. Locks the wire format
// to the same arithmetic the reaggregators use.
func TestChunk_View(t *testing.T) {
	b := Chunk{
		RouteID:         "7",
		Date:            time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC),
		Band:            "morning",
		ServiceKind:     "weekday",
		TripCount:       100,
		OnTimeCount:     75,
		ScheduledCount:  108,
		CancelledCount:  6,
		NoNoticeCount:   2,
		HeadwayCount:    textbookHeadwayCount,
		HeadwaySumSec:   textbookSumH,
		HeadwaySumSecSq: textbookSumHSq,
		SchedHeadwaySec: textbookSchedHeadwaySec,
	}
	v := b.View()

	// Identity
	if v.RouteID != "7" {
		t.Errorf("RouteID: got %q, want %q", v.RouteID, "7")
	}
	if v.Date != "2026-03-14" {
		t.Errorf("Date: got %q, want %q", v.Date, "2026-03-14")
	}
	if v.Band != "morning" {
		t.Errorf("Band: got %q, want %q", v.Band, "morning")
	}

	// Raw counts mirror the chunk
	if v.Trips != 100 || v.OnTime != 75 || v.Scheduled != 108 || v.Cancelled != 6 || v.NoNotice != 2 {
		t.Errorf("raw counts didn't round-trip: %+v", v)
	}
	if v.HeadwayN != textbookHeadwayCount || v.HeadwaySum != textbookSumH || v.HeadwaySumSq != textbookSumHSq {
		t.Errorf("headway sums didn't round-trip: %+v", v)
	}
	if v.SchedHeadway != textbookSchedHeadwaySec {
		t.Errorf("SchedHeadway: got %v, want %v", v.SchedHeadway, textbookSchedHeadwaySec)
	}

	// Pre-computed display values match the formulas
	almostEqual(t, "OTPPct", v.OTPPct, 75.0, 0.001)      // 75 / 100 * 100
	almostEqual(t, "EWTMin", v.EWTMin, 58.0/60.0, 0.001) // 58 sec / 60
	almostEqual(t, "Cv", v.Cv, 0.39440, 0.0005)          // textbook
}

// TestChunk_View_NoTripsClampsOTP confirms that a chunk with zero trips
// renders OTPPct as 0 (not NaN) — the JSON has to be safe to deserialize
// even for empty bands like a Sunday route that didn't run.
func TestChunk_View_NoTripsClampsOTP(t *testing.T) {
	b := Chunk{
		RouteID: "7",
		Date:    time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		Band:    "morning",
	}
	v := b.View()
	if v.OTPPct != 0 {
		t.Errorf("empty chunk OTPPct: got %v, want 0", v.OTPPct)
	}
	if v.Cv != 0 {
		t.Errorf("empty chunk Cv: got %v, want 0", v.Cv)
	}
	if v.EWTMin != 0 {
		t.Errorf("empty chunk EWTMin: got %v, want 0", v.EWTMin)
	}
}

// TestKPI_RouteBSingleHeadway guards the "not enough headway obs" branch:
// Route B's only headway means MetricCv should skip it entirely, so a
// KPI call restricted to Route B returns ok=false for Cv even though
// the route has OTP data.
func TestKPI_RouteBSingleHeadway(t *testing.T) {
	var routeB []ChunkView
	for _, c := range textbookChunks() {
		if c.RouteID == "B" {
			routeB = append(routeB, c)
		}
	}
	if v, ok := KPI(routeB, MetricOTP, "morning"); !ok || math.Abs(v-75.0) > 0.001 {
		t.Errorf("route B OTP: got %v ok=%v, want 75", v, ok)
	}
	if _, ok := KPI(routeB, MetricCv, "morning"); ok {
		t.Error("route B Cv: expected ok=false (single headway)")
	}
}
