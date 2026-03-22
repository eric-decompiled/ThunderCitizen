// Package chunk is the metrics-math home for ThunderCitizen's transit
// performance system. It exists as a standalone subpackage so a reviewer
// (or auditor, or curious councillor) can read every formula in one place
// without wading through DB code, HTTP handlers, or schema definitions.
//
// What lives here:
//
//   - The Chunk data type — one (route_id, date, band) row of the raw
//     counts and sums that drive every metric. A chunk is 6 hours of one
//     route's stats.
//   - Pure formulas — Cv, EWTSec, WaitMin. Each takes primitive numbers,
//     returns a primitive number, and has a textbook reference in its
//     doc comment. See math.go and math_test.go.
//   - Reaggregators — ComputeSystem and ComputeRoutes. They take a slice
//     of Chunk values and produce per-band / per-route summaries via
//     SUM-stable identities. They return plain primitives so the
//     surrounding transit package can adapt them to its own API types.
//     See rollup.go.
//
// What does NOT live here:
//
//   - Database queries. The transit package's BuildChunk runs the SQL
//     that turns Tier 3 event tables into a Chunk; this package never
//     touches a *pgxpool.Pool.
//   - HTTP handlers, JSON formatting, cache layers. Those wrap the math
//     but aren't the math.
//
// Verifying our work:
//
// math_test.go contains a TfL textbook example (headways 5, 7, 3, 10, 5
// minutes) with hand-computed Cv ≈ 0.394 and EWT = 58 seconds. Any change
// to the formulas should keep that test green. The pooling-identity tests
// prove that summing two chunks' columns and re-running the formula gives
// the same answer as treating it as one big sample — this is the
// load-bearing property that lets ComputeSystem combine chunks across
// the date range without losing precision.
package chunk

import "time"

// Chunk is one row of the raw counts and sums that drive every metric
// for a single (route_id, date, band) triple. It is the unit of work for
// ComputeSystem and ComputeRoutes, and the on-disk row format for the
// transit.route_band_chunk table.
//
// Counts and sums are stored, not rates: that's what makes reaggregation
// across multiple chunks exact arithmetic. SUM(on_time_count) /
// SUM(trip_count) is the same number you'd get from re-running the OTP
// query against the underlying observations directly.
type Chunk struct {
	RouteID     string
	Date        time.Time
	Band        string // "morning"|"midday"|"evening"
	ServiceKind string // "weekday"|"saturday"|"sunday"

	// OTP — on-time performance.
	TripCount   int // distinct trips that had at least one timepoint observation
	OnTimeCount int // trips with average delay in [OTPEarlyLimit, OTPLateLimit]

	// Cancel rate / notice.
	ScheduledCount int // from transit.route_baseline at build time
	CancelledCount int // distinct trips cancelled in the band
	NoNoticeCount  int // cancellations with lead < 15 minutes

	// Per-route headway gap statistics. Used by Cv (which is per-route)
	// and by per-route Wait/EWT drill-downs.
	HeadwayCount    int     // number of valid gaps observed
	HeadwaySumSec   float64 // SUM(h)
	HeadwaySumSecSq float64 // SUM(h^2)
	SchedHeadwaySec float64 // scheduled headway from route_baseline (sec)

	BuiltAt time.Time
}

// On-time window: industry standard -1 min / +5 min. These constants live
// here (not in the DB code) so the textbook tests and the SQL params share
// a single source of truth.
const (
	OTPEarlyLimit = -60.0 // 1 minute early (seconds)
	OTPLateLimit  = 300.0 // 5 minutes late (seconds)
)

// ChunkView is the JSON-serializable shape of a Chunk. It's the wire
// format the transit page templates embed via @templ.JSONScript so the
// frontend has chunk data on first paint without a fetch.
//
// Two-track design (raw counts AND pre-computed display values):
//
//   - The raw count fields (Trips, OnTime, Scheduled, Cancelled, NoNotice,
//     HeadwayN, HeadwaySum, HeadwaySumSq, SchedHeadway) are what the
//     frontend SUMs across multiple chunks when aggregating to system /
//     route / day-level numbers. SUM-ing raw counts then dividing is exact
//     trip-weighted arithmetic; SUM-ing pre-computed percentages is wrong.
//
//   - The pre-computed display fields (OTPPct, EWTMin, Cv) are convenient
//     when rendering ONE chunk directly — a per-day cell in a heatmap, an
//     individual row in a drill-down, etc. They are computed by the same
//     math.go formulas the reaggregators use, so a single-chunk render and
//     a 1-chunk aggregation produce identical numbers.
//
// Date is a "YYYY-MM-DD" string (not time.Time) so the JSON is human-
// readable and round-trips through JS without timezone surprises.
type ChunkView struct {
	// Identity
	RouteID string `json:"route_id"`
	Date    string `json:"date"` // YYYY-MM-DD
	Band    string `json:"band"` // "morning" | "midday" | "evening"

	// Raw counts and sums — JS reaggregates by SUM-ing these across chunks.
	Trips        int     `json:"trips"`
	OnTime       int     `json:"on_time"`
	Scheduled    int     `json:"scheduled"`
	Cancelled    int     `json:"cancelled"`
	NoNotice     int     `json:"no_notice"`
	HeadwayN     int     `json:"headway_n"`
	HeadwaySum   float64 `json:"headway_sum_sec"`
	HeadwaySumSq float64 `json:"headway_sum_sq_sec"`
	SchedHeadway float64 `json:"sched_headway_sec"`

	// Pre-computed display values for direct rendering of a single chunk.
	// Do NOT use these when summing multiple chunks together — sum the
	// raw fields above and re-apply the formula instead.
	OTPPct float64 `json:"otp_pct"` // 0..100, 0 if no trips
	EWTMin float64 `json:"ewt_min"` // minutes
	Cv     float64 `json:"cv"`      // dimensionless
}

// View returns the JSON-serializable view of a Chunk. The pre-computed
// display values come from the same Cv / EWTSec formulas in math.go that
// the reaggregators use, so a Chunk and its View always agree.
func (c Chunk) View() ChunkView {
	v := ChunkView{
		RouteID:      c.RouteID,
		Date:         c.Date.Format("2006-01-02"),
		Band:         c.Band,
		Trips:        c.TripCount,
		OnTime:       c.OnTimeCount,
		Scheduled:    c.ScheduledCount,
		Cancelled:    c.CancelledCount,
		NoNotice:     c.NoNoticeCount,
		HeadwayN:     c.HeadwayCount,
		HeadwaySum:   c.HeadwaySumSec,
		HeadwaySumSq: c.HeadwaySumSecSq,
		SchedHeadway: c.SchedHeadwaySec,
	}
	if c.TripCount > 0 {
		v.OTPPct = float64(c.OnTimeCount) * 100.0 / float64(c.TripCount)
	}
	v.EWTMin = EWTSec(c.HeadwaySumSec, c.HeadwaySumSecSq, c.SchedHeadwaySec) / 60.0
	v.Cv = Cv(c.HeadwayCount, c.HeadwaySumSec, c.HeadwaySumSecSq)
	return v
}
