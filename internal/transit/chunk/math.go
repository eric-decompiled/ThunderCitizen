package chunk

import "math"

// Cv returns the coefficient of variation of headway gaps — stddev divided
// by mean — for a single chunk, or for any sum of chunks (the formula is
// SUM-stable). Lower means buses arrive at more regular intervals; higher
// means clumpy bunching and big gaps.
//
// Implementation uses the stddev-from-sums identity:
//
//	Var(X) = E(X^2) - E(X)^2
//	       = sum(x^2)/n - (sum(x)/n)^2
//
// so we never need to touch the raw headway list. Returns 0 for fewer than
// 2 observations (a coefficient of variation isn't defined on a single
// sample, and dividing by zero would NaN out the dashboard).
//
// Reference: any introductory statistics text. The TfL Service Performance
// methodology and Furth & Muller (2006) both cite Cv as the headline
// service-regularity metric for high-frequency routes (~10 min headways
// or shorter).
func Cv(headwayCount int, sumH, sumHSq float64) float64 {
	if headwayCount < 2 || sumH <= 0 {
		return 0
	}
	n := float64(headwayCount)
	mean := sumH / n
	variance := sumHSq/n - mean*mean
	if variance < 0 {
		// Numerical drift can push this microscopically below zero on
		// near-uniform data. Treat as zero.
		variance = 0
	}
	return math.Sqrt(variance) / mean
}

// EWTSec returns Excess Wait Time, in seconds, for one chunk (or any sum
// of chunks). EWT measures how much longer a typical rider waits than the
// schedule promised — it captures both irregularity and total trip
// shortfall in one number.
//
// Methodology (TfL Service Performance, also Furth & Muller 2006):
//
//	AWT = sum(h^2) / (2 * sum(h))   // "actual wait" — irregularity-weighted
//	SWT = scheduled_headway / 2     // "scheduled wait" — perfect-service ideal
//	EWT = max(0, AWT - SWT)
//
// AWT comes from a probability argument: if a rider arrives at the stop at
// a uniformly random time, the expected gap they fall into has length
// E[H^2]/E[H], and they wait half of that on average — so the per-rider
// average wait is E[H^2] / (2*E[H]) = sum(h^2) / (2*sum(h)). This is
// strictly larger than the simple mean/2 whenever headways are irregular,
// which is why EWT is more honest than "average gap divided by two".
//
// EWT clamps to zero when AWT < SWT — riders don't get credit for buses
// arriving more frequently than scheduled.
func EWTSec(sumH, sumHSq, schedHeadwaySec float64) float64 {
	if sumH <= 0 || schedHeadwaySec <= 0 {
		return 0
	}
	awt := sumHSq / (2.0 * sumH)
	swt := schedHeadwaySec / 2.0
	if awt <= swt {
		return 0
	}
	return awt - swt
}

// WaitMin returns the simple mean of headway gaps in a chunk, in minutes.
// This is what a rider with no schedule knowledge would experience as the
// average gap between buses on this route at one of its timepoint stops.
//
// Per-route: each chunk is one route × one band × one day. System-level
// aggregation across routes happens by SUM-ing headway_count and
// headway_sum_sec across the relevant chunks before dividing — done in
// KPIFromChunks (Go) and chunks.js (JS frontend).
func WaitMin(headwayCount int, sumH float64) float64 {
	if headwayCount == 0 {
		return 0
	}
	return (sumH / float64(headwayCount)) / 60.0
}
