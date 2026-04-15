package views

import (
	"fmt"
	"math"
	"strings"
	"time"

	"thundercitizen/internal/metrics"
)

// HealthViewModel drives the /health page. Built from the metrics
// snapshot plus container-baked env (TC_IMAGE) and ldflag-baked build
// metadata passed in by the handler.
type HealthViewModel struct {
	Image     string
	Commit    string
	BuildTime string
	BootAt    time.Time
	Uptime    time.Duration

	Routes             []RouteCount
	MaxCount           uint64
	Total              uint64
	Err4xxAll          uint64
	Err4xxAllHeightPct int // 0..100, normalized against MaxCount
	Err5xxAll          uint64

	P50 time.Duration
	P90 time.Duration
	P99 time.Duration

	Latency LatencyChart
}

// LatencyChart holds the pre-computed geometry for the /health latency
// SVG. All coordinates are in the SVG's internal viewBox units
// (LatencyChartWidth x LatencyChartHeight), so the template only has
// to paste strings in.
type LatencyChart struct {
	Path     string        // smooth curve, SVG path "d" attribute
	FillPath string        // closed polygon for the area below the curve
	Dots     []ChartPoint  // discrete data points, one per bucket
	Ticks    []LatencyTick // x-axis tick labels
	P50X     int
	P90X     int
	P99X     int
	MaxCount uint64
	Total    uint64
	HasData  bool
}

// ChartPoint is a discrete point in SVG viewBox coordinates.
type ChartPoint struct {
	X int
	Y int
}

// LatencyTick is one labeled tick on the x-axis of the latency chart.
type LatencyTick struct {
	X     int
	Label string
}

// LatencyChart viewBox dimensions. Kept as exported constants so the
// templ template can use them verbatim.
const (
	LatencyChartWidth  = 800
	LatencyChartHeight = 140
)

// RouteCount is one row/bar in the histogram.
type RouteCount struct {
	Pattern   string
	Total     uint64
	OK        uint64
	Err4xx    uint64
	Err5xx    uint64
	HeightPct int // 0..100, snapped to nearest 5
	P90       time.Duration
}

// NewHealthViewModel composes the view model. Pass the image string
// and build metadata in so this package doesn't reach back into env or
// the handlers package.
func NewHealthViewModel(image, commit, buildTime string) HealthViewModel {
	snap := metrics.Read()

	rows := make([]RouteCount, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		height := 0
		if snap.MaxCount > 0 {
			height = snapToNearest5(int(r.Total * 100 / snap.MaxCount))
		}
		rows = append(rows, RouteCount{
			Pattern:   r.Pattern,
			Total:     r.Total,
			OK:        r.OK,
			Err4xx:    r.Err4xx,
			Err5xx:    r.Err5xx,
			HeightPct: height,
			P90:       metrics.Percentile(r.Latency[:], 90),
		})
	}

	err4xxHeight := 0
	if snap.MaxCount > 0 {
		err4xxHeight = snapToNearest5(int(snap.Err4xxAll * 100 / snap.MaxCount))
	}

	latency := buildLatencyChart(snap.GlobalLatency[:])

	return HealthViewModel{
		Image:              image,
		Commit:             commit,
		BuildTime:          buildTime,
		BootAt:             snap.BootAt,
		Uptime:             snap.Uptime,
		Routes:             rows,
		MaxCount:           snap.MaxCount,
		Total:              snap.Total,
		Err4xxAll:          snap.Err4xxAll,
		Err4xxAllHeightPct: err4xxHeight,
		Err5xxAll:          snap.Err5xxAll,

		P50:     metrics.Percentile(snap.GlobalLatency[:], 50),
		P90:     metrics.Percentile(snap.GlobalLatency[:], 90),
		P99:     metrics.Percentile(snap.GlobalLatency[:], 99),
		Latency: latency,
	}
}

// buildLatencyChart turns a bucket histogram into SVG geometry ready
// for the templ template. Y-axis uses a sqrt scale so a single spiky
// bucket doesn't flatten the rest of the distribution. The line is
// smoothed with a Catmull-Rom-to-Bezier spline so the curve flows
// instead of zig-zagging.
func buildLatencyChart(buckets []uint64) LatencyChart {
	n := len(buckets)
	var maxBucket, total uint64
	for _, c := range buckets {
		total += c
		if c > maxBucket {
			maxBucket = c
		}
	}
	if total == 0 || n < 2 {
		return LatencyChart{}
	}

	w := float64(LatencyChartWidth)
	h := float64(LatencyChartHeight)
	chartTop := 18.0      // leave room at the top for p-labels + max badge
	chartBottom := h - 20 // leave room at the bottom for x-axis labels
	usableH := chartBottom - chartTop
	step := w / float64(n-1)

	dots := make([]ChartPoint, n)
	for i, c := range buckets {
		x := int(float64(i) * step)
		norm := math.Sqrt(float64(c) / float64(maxBucket))
		y := int(chartBottom - norm*usableH)
		dots[i] = ChartPoint{X: x, Y: y}
	}

	curve := smoothCurveCommands(dots, 6.0)
	linePath := fmt.Sprintf("M%d,%d%s", dots[0].X, dots[0].Y, curve)
	fillPath := fmt.Sprintf("M0,%d L%d,%d%s L%d,%d Z",
		int(chartBottom),
		dots[0].X, dots[0].Y,
		curve,
		int(w), int(chartBottom),
	)

	tickDurations := []time.Duration{
		500 * time.Microsecond,
		time.Millisecond,
		5 * time.Millisecond,
		10 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
	}
	bounds := metrics.LatencyBucketBounds()
	var ticks []LatencyTick
	for _, td := range tickDurations {
		idx := durationBucketIndex(td, bounds)
		if idx < 0 || idx >= n {
			continue
		}
		ticks = append(ticks, LatencyTick{
			X:     int(float64(idx) * step),
			Label: FormatDuration(td),
		})
	}

	p50idx := durationBucketIndex(metrics.Percentile(buckets, 50), bounds)
	p90idx := durationBucketIndex(metrics.Percentile(buckets, 90), bounds)
	p99idx := durationBucketIndex(metrics.Percentile(buckets, 99), bounds)

	return LatencyChart{
		Path:     linePath,
		FillPath: fillPath,
		Dots:     dots,
		Ticks:    ticks,
		P50X:     int(float64(p50idx) * step),
		P90X:     int(float64(p90idx) * step),
		P99X:     int(float64(p99idx) * step),
		MaxCount: maxBucket,
		Total:    total,
		HasData:  true,
	}
}

// smoothCurveCommands returns the " C cp1 cp2 p ..." commands that
// connect each point via a Catmull-Rom-to-Bezier conversion. The
// caller prepends the leading "M" command. tension controls how
// tightly the curve hugs the control polygon — 6 is a gentle smooth.
func smoothCurveCommands(pts []ChartPoint, tension float64) string {
	if len(pts) < 2 {
		return ""
	}
	var b strings.Builder
	if len(pts) == 2 {
		fmt.Fprintf(&b, " L%d,%d", pts[1].X, pts[1].Y)
		return b.String()
	}
	for i := 0; i < len(pts)-1; i++ {
		p1 := pts[i]
		p2 := pts[i+1]
		var p0, p3 ChartPoint
		if i == 0 {
			p0 = p1
		} else {
			p0 = pts[i-1]
		}
		if i+2 < len(pts) {
			p3 = pts[i+2]
		} else {
			p3 = p2
		}
		cp1x := float64(p1.X) + (float64(p2.X)-float64(p0.X))/tension
		cp1y := float64(p1.Y) + (float64(p2.Y)-float64(p0.Y))/tension
		cp2x := float64(p2.X) - (float64(p3.X)-float64(p1.X))/tension
		cp2y := float64(p2.Y) - (float64(p3.Y)-float64(p1.Y))/tension
		fmt.Fprintf(&b, " C%.1f,%.1f %.1f,%.1f %d,%d", cp1x, cp1y, cp2x, cp2y, p2.X, p2.Y)
	}
	return b.String()
}

// durationBucketIndex finds the first bucket index whose upper bound
// is >= d. Returns len(bounds) (overflow) if d is larger than every
// bound. Returns -1 if d is zero (empty signal).
func durationBucketIndex(d time.Duration, bounds []time.Duration) int {
	if d <= 0 {
		return -1
	}
	for i, b := range bounds {
		if d <= b {
			return i
		}
	}
	return len(bounds)
}

// FormatDuration renders a Duration as a compact "12ms" / "340µs" /
// "1.4s" string for the health page.
func FormatDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "–"
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// FormatUptime renders a Duration as a compact "1d 4h 12m 3s" string.
// Drops zero-value leading fields so a fresh boot shows "12s" rather
// than "0d 0h 0m 12s".
func FormatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	d -= time.Duration(mins) * time.Minute
	secs := int(d / time.Second)

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	parts = append(parts, fmt.Sprintf("%ds", secs))
	return strings.Join(parts, " ")
}

// snapToNearest5 rounds a 0..100 height percentage to the nearest 5%.
// The chart uses a small set of CSS height classes rather than dynamic
// inline styles (templ rejects unsanitized style attributes), so this
// keeps the generated CSS to ~21 rules instead of 101.
func snapToNearest5(n int) int {
	if n <= 0 {
		return 0
	}
	if n >= 100 {
		return 100
	}
	snapped := ((n + 2) / 5) * 5
	if snapped == 0 {
		return 5
	}
	return snapped
}
