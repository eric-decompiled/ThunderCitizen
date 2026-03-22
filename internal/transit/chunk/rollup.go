package chunk

// Bands is the canonical list of band names in display order. The chunk
// package treats bands as opaque strings; the Band struct with start/end
// hour bounds lives in internal/transit/metrics.go next to the SQL that
// needs the bounds.
var Bands = []string{"morning", "midday", "evening"}

// Metric identifies one of the six KPIs the transit metrics page reports.
// String-valued so templates and JSON round-trip it without ceremony.
type Metric string

const (
	MetricOTP    Metric = "otp"
	MetricCancel Metric = "cancel"
	MetricNotice Metric = "notice"
	MetricWait   Metric = "wait"
	MetricEWT    Metric = "ewt"
	MetricCv     Metric = "cv"
)

// KPI is the single source of truth for reducing a slice of chunk views
// into one KPI reading. The templates call it via KPIFromChunks in the
// transit package; the JS frontend mirrors it verbatim in
// static/transit/chunks.js.
//
// Pass band == "" to pool every band in the slice, or "morning" /
// "midday" / "evening" to restrict. Returns (value, true) when there's
// enough data, (0, false) otherwise — templates render "—" for the false
// case.
//
// Aggregation rules:
//
//	OTP, Cancel, Notice
//	  Trip-weighted SUM of raw counts, divided once. Pooling across
//	  routes is fine — these are absolute counts in compatible units.
//
//	Wait
//	  Pooled mean gap: SUM(h) / SUM(n) across every in-filter chunk.
//	  Every observed headway gets one vote.
//
//	Cv, EWT
//	  Per-route pool first (sum the route's headway columns across the
//	  in-filter chunks), compute the per-route number, then take the
//	  simple mean across routes with enough data. Each route gets one
//	  vote regardless of how many observations it contributed.
//
//	  Why per-route: pooling multiple routes at a stop introduces
//	  artificial variance from interleaving — a perfectly regular
//	  20-min Route 1 and a perfectly regular 30-min Route 5 produce
//	  a jagged 2/18/12/8-minute pooled gap distribution. See
//	  docs/transit-metrics.md §6.
//
// Units:
//
//	OTP, Cancel, Notice: percent (0..100)
//	Wait, EWT:           minutes
//	Cv:                  dimensionless ratio
func KPI(chunks []ChunkView, metric Metric, band string) (float64, bool) {
	inBand := func(b ChunkView) bool { return band == "" || b.Band == band }

	switch metric {
	case MetricOTP, MetricCancel, MetricNotice:
		var trips, onTime, scheduled, cancelled, noNotice int
		for i := range chunks {
			if !inBand(chunks[i]) {
				continue
			}
			trips += chunks[i].Trips
			onTime += chunks[i].OnTime
			scheduled += chunks[i].Scheduled
			cancelled += chunks[i].Cancelled
			noNotice += chunks[i].NoNotice
		}
		switch metric {
		case MetricOTP:
			if trips == 0 {
				return 0, false
			}
			return float64(onTime) * 100.0 / float64(trips), true
		case MetricCancel:
			if scheduled == 0 {
				return 0, false
			}
			return float64(cancelled) * 100.0 / float64(scheduled), true
		case MetricNotice:
			if cancelled == 0 {
				return 0, false
			}
			return float64(noNotice) * 100.0 / float64(cancelled), true
		}

	case MetricWait:
		var n int
		var sumH float64
		for i := range chunks {
			if !inBand(chunks[i]) {
				continue
			}
			n += chunks[i].HeadwayN
			sumH += chunks[i].HeadwaySum
		}
		if n == 0 {
			return 0, false
		}
		return WaitMin(n, sumH), true

	case MetricCv, MetricEWT:
		type routeAcc struct {
			n        int
			sumH     float64
			sumHSq   float64
			schedSum float64
			schedN   int
		}
		perRoute := map[string]*routeAcc{}
		for i := range chunks {
			if !inBand(chunks[i]) {
				continue
			}
			a, ok := perRoute[chunks[i].RouteID]
			if !ok {
				a = &routeAcc{}
				perRoute[chunks[i].RouteID] = a
			}
			a.n += chunks[i].HeadwayN
			a.sumH += chunks[i].HeadwaySum
			a.sumHSq += chunks[i].HeadwaySumSq
			if chunks[i].SchedHeadway > 0 {
				a.schedSum += chunks[i].SchedHeadway
				a.schedN++
			}
		}

		var sum float64
		var k int
		if metric == MetricCv {
			for _, a := range perRoute {
				if a.n < 2 {
					continue
				}
				sum += Cv(a.n, a.sumH, a.sumHSq)
				k++
			}
		} else {
			for _, a := range perRoute {
				if a.n < 1 || a.schedN == 0 {
					continue
				}
				sched := a.schedSum / float64(a.schedN)
				sum += EWTSec(a.sumH, a.sumHSq, sched) / 60.0
				k++
			}
		}
		if k == 0 {
			return 0, false
		}
		return sum / float64(k), true
	}

	return 0, false
}
