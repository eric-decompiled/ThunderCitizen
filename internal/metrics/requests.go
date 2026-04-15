// Package metrics holds an in-memory request counter that feeds the
// /health page's route histogram. Everything here resets on restart by
// design — no persistence, no cross-process aggregation. Volume on this
// site is low enough that a single mutex-guarded map is fine; swap to
// sync.Map or atomics only if a profile shows the lock mattering.
package metrics

import (
	"context"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// unmatchedKey is the bucket used when chi did not match a route — almost
// always bot scans and our themed 404 catch-all. Broken out so the chart
// doesn't silently swallow them.
const unmatchedKey = "(unmatched)"

// Latency buckets are log-spaced so the line chart has resolution in
// the 1-200ms range where most web-app traffic lives while still
// covering pathological outliers out to ~2s.
const (
	numLatencyBuckets  = 36
	latencyBucketCount = numLatencyBuckets + 1 // +1 overflow
	latencyBucketFirst = 200 * time.Microsecond
	latencyBucketLast  = 2 * time.Second
)

// latencyBuckets holds the exclusive upper bound of each non-overflow
// bucket. A request with duration d falls in bucket i where
// d < latencyBuckets[i]. Anything slower than the final bound goes
// into the overflow bucket at index numLatencyBuckets.
var latencyBuckets [numLatencyBuckets]time.Duration

func init() {
	base := float64(latencyBucketFirst)
	end := float64(latencyBucketLast)
	ratio := math.Pow(end/base, 1.0/float64(numLatencyBuckets-1))
	for i := 0; i < numLatencyBuckets; i++ {
		latencyBuckets[i] = time.Duration(base * math.Pow(ratio, float64(i)))
	}
}

// LatencyBucketBounds returns a copy of the upper bounds for the
// non-overflow buckets. Views use this to render x-axis tick labels.
func LatencyBucketBounds() []time.Duration {
	out := make([]time.Duration, numLatencyBuckets)
	copy(out, latencyBuckets[:])
	return out
}

type routeStat struct {
	Total   uint64
	OK      uint64
	Err4xx  uint64
	Err5xx  uint64
	Latency [latencyBucketCount]uint64
}

var (
	mu     sync.Mutex
	counts = map[string]*routeStat{}
	bootAt = time.Now()
)

// SetBootTime overrides the package's boot timestamp. Call from main
// before ListenAndServe so /health uptime starts at server start, not
// at package-init time.
func SetBootTime(t time.Time) {
	mu.Lock()
	defer mu.Unlock()
	bootAt = t
}

// RouteSnapshot is one entry in the exported snapshot.
type RouteSnapshot struct {
	Pattern string
	Total   uint64
	OK      uint64
	Err4xx  uint64
	Err5xx  uint64
	Latency [latencyBucketCount]uint64
}

// Snapshot returns a point-in-time copy of the counters, sorted by
// Total descending (with the unmatched bucket pinned last regardless of
// its count so it can't crowd out real routes visually).
type Snapshot struct {
	BootAt        time.Time
	Uptime        time.Duration
	Routes        []RouteSnapshot
	Total         uint64
	Err4xxAll     uint64
	Err5xxAll     uint64
	MaxCount      uint64
	GlobalLatency [latencyBucketCount]uint64
}

// Read returns a snapshot of the counters.
func Read() Snapshot {
	mu.Lock()
	defer mu.Unlock()

	out := Snapshot{
		BootAt: bootAt,
		Uptime: time.Since(bootAt),
		Routes: make([]RouteSnapshot, 0, len(counts)),
	}
	for pattern, s := range counts {
		rs := RouteSnapshot{
			Pattern: pattern,
			Total:   s.Total,
			OK:      s.OK,
			Err4xx:  s.Err4xx,
			Err5xx:  s.Err5xx,
			Latency: s.Latency,
		}
		out.Routes = append(out.Routes, rs)
		out.Total += s.Total
		out.Err4xxAll += s.Err4xx
		out.Err5xxAll += s.Err5xx
		if s.Total > out.MaxCount {
			out.MaxCount = s.Total
		}
		for i, c := range s.Latency {
			out.GlobalLatency[i] += c
		}
	}

	sort.Slice(out.Routes, func(i, j int) bool {
		a, b := out.Routes[i], out.Routes[j]
		// Pin unmatched last so a scan storm can't dominate the chart.
		if a.Pattern == unmatchedKey {
			return false
		}
		if b.Pattern == unmatchedKey {
			return true
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Pattern < b.Pattern
	})

	return out
}

// statusWriter mirrors the one in internal/middleware/middleware.go —
// kept separate so metrics can be imported without pulling the whole
// middleware package into packages that only need the counter.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware records one observation per request: the matched chi
// route pattern (or the unmatched bucket) plus the response status
// class. Must be registered via r.Use. Pre-injects a RouteContext into
// the request context so chi's mux populates the object we can observe
// post-dispatch — otherwise chi would allocate its own internal one on
// a request copy we'd never see.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start)

		pattern := unmatchedKey
		if p := rctx.RoutePattern(); p != "" {
			pattern = p
		}

		// Skip high-volume / low-signal buckets:
		//   - /static/*  — dozens of hits per page load, dominates chart
		//   - /health    — Docker polls every 30s, would self-dominate
		//   - SSE stream — long-lived connection, would peg p90/p99 to
		//     the overflow bucket and misrepresent real request latency
		if pattern == "/static/*" ||
			pattern == "/health" ||
			pattern == "/api/transit/vehicles/stream" {
			return
		}

		idx := latencyBucketIndex(dur)

		mu.Lock()
		s := counts[pattern]
		if s == nil {
			s = &routeStat{}
			counts[pattern] = s
		}
		s.Total++
		switch {
		case sw.status >= 500:
			s.Err5xx++
		case sw.status >= 400:
			s.Err4xx++
		default:
			s.OK++
		}
		s.Latency[idx]++
		mu.Unlock()
	})
}

// latencyBucketIndex returns the bucket index for a request duration.
// Uses a linear scan because the bucket slice is short (10 entries) —
// cheaper than a binary search's overhead at this size.
func latencyBucketIndex(d time.Duration) int {
	for i, b := range latencyBuckets {
		if d < b {
			return i
		}
	}
	return len(latencyBuckets) // overflow
}

// Percentile estimates the pth percentile of the bucket histogram,
// returning the upper bound of the bucket where the cumulative count
// crosses p/100. For the overflow bucket the upper bound is returned
// as the last defined bucket's upper bound — percentile estimates are
// inherently lossy at bucket boundaries. p is in [0, 100].
func Percentile(buckets []uint64, p float64) time.Duration {
	var total uint64
	for _, c := range buckets {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := uint64(float64(total) * p / 100.0)
	if target == 0 {
		target = 1
	}
	var cum uint64
	for i, c := range buckets {
		cum += c
		if cum >= target {
			if i < len(latencyBuckets) {
				return latencyBuckets[i]
			}
			return latencyBuckets[len(latencyBuckets)-1]
		}
	}
	return latencyBuckets[len(latencyBuckets)-1]
}
