// perftest hits every route on the local server and prints a latency report.
//
// Usage:
//
//	go run ./cmd/perftest              # 10 runs per route, print report
//	go run ./cmd/perftest -n 25        # 25 runs per route
//	go run ./cmd/perftest -r           # record to perftest/ + show delta vs last run
//	go run ./cmd/perftest -base http://staging:8080
//
// Records are saved to perftest/ as timestamped JSON. When a previous record
// exists, the report includes a Δ Avg column showing regressions (+) and
// improvements (-) against the last run.
//
// Routes are auto-discovered: transit route IDs from /api/transit/routes,
// minutes IDs from page links.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const recordDir = "perftest"

type route struct {
	group string
	path  string
}

type result struct {
	route route
	code  int
	times []time.Duration
	err   error
}

// record is the JSON-serializable format for saved runs.
type record struct {
	Timestamp string        `json:"timestamp"`
	Base      string        `json:"base"`
	Runs      int           `json:"runs"`
	Routes    []recordRoute `json:"routes"`
}

type recordRoute struct {
	Group string `json:"group"`
	Path  string `json:"path"`
	Code  int    `json:"code"`
	Min   int64  `json:"min_ms"`
	Avg   int64  `json:"avg_ms"`
	Med   int64  `json:"med_ms"`
	P95   int64  `json:"p95_ms"`
	Max   int64  `json:"max_ms"`
	Error string `json:"error,omitempty"`
}

func main() {
	base := flag.String("base", "http://localhost:8080", "server base URL")
	n := flag.Int("n", 10, "requests per route")
	save := flag.Bool("r", false, "record results to "+recordDir+"/")
	flag.Parse()

	// Health check
	resp, err := http.Get(*base + "/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "server not reachable at %s: %v\n", *base, err)
		os.Exit(1)
	}
	resp.Body.Close()

	// Discover parameterized route values from running server.
	routeIDs := discoverRouteIDs(*base)
	minutesID := discoverMinutesID(*base)

	routes := buildRoutes(routeIDs, minutesID)

	// Load previous record for delta comparison.
	prev := loadLatestRecord()
	prevLookup := map[string]recordRoute{}
	if prev != nil {
		for _, rr := range prev.Routes {
			prevLookup[rr.Path] = rr
		}
	}

	hasDelta := len(prevLookup) > 0
	fmt.Printf("perftest: %d routes × %d runs against %s\n", len(routes), *n, *base)
	if hasDelta {
		fmt.Printf("  comparing against %s\n", prev.Timestamp)
	}

	// Run benchmarks grouped by section.
	results := make([]result, 0, len(routes))
	curGroup := ""
	for _, r := range routes {
		if r.group != curGroup {
			curGroup = r.group
			printGroupHeader(curGroup, hasDelta)
		}
		res := bench(*base, r, *n)
		results = append(results, res)
		printResult(res, prevLookup)
	}

	// Summary
	fmt.Println()
	printSummary(results)

	// Save record
	if *save {
		path, err := saveRecord(*base, *n, results)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n  record save failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\n  Recorded to %s\n", path)
	}
}

func buildRoutes(routeIDs []string, minutesID string) []route {
	routes := []route{
		// --- App pages ---
		{"Pages", "/"},
		{"Pages", "/budget"},
		{"Pages", "/councillors"},
		{"Pages", "/minutes"},
		{"Pages", "/about"},
		{"Pages", "/health"},
		{"Pages", "/version"},
	}

	// Minutes detail
	if minutesID != "" {
		routes = append(routes, route{"Pages", "/minutes/" + minutesID})
	}

	// Searches
	routes = append(routes, []route{
		{"Search", "/minutes?q=transit"},
		{"Search", "/minutes?q=budget"},
		{"Search", "/minutes?q=housing+affordable"},
		{"Search", "/minutes?term=2022"},
		{"Search", "/minutes?votes=1"},
		{"Search", "/minutes?defeated=1"},
		{"Search", "/councillors?term=2018"},
	}...)

	// Transit pages
	routes = append(routes, []route{
		{"Transit Pages", "/transit/"},
		{"Transit Pages", "/transit/metrics"},
		{"Transit Pages", "/transit/routes"},
		{"Transit Pages", "/transit/method"},
	}...)
	for _, id := range routeIDs {
		routes = append(routes, route{"Transit Pages", "/transit/route/" + id})
	}

	// Transit API
	routes = append(routes, []route{
		{"Transit API", "/api/transit/vehicles"},
		{"Transit API", "/api/transit/vehicles.json"},
		{"Transit API", "/api/transit/stats"},
		{"Transit API", "/api/transit/stats?range=percentiles"},
		{"Transit API", "/api/transit/stats?range=week"},
		{"Transit API", "/api/transit/metrics"},
		{"Transit API", "/api/transit/kpis"},
		{"Transit API", "/api/transit/stops"},
		{"Transit API", "/api/transit/trends"},
		{"Transit API", "/api/transit/routes"},
		{"Transit API", "/api/transit/coverage"},
		{"Transit API", "/api/transit/timepoints"},
		{"Transit API", "/api/transit/stops/nearby?lat=48.38&lon=-89.25"},
		{"Transit API", "/api/transit/stops/analytics"},
		{"Transit API", "/api/transit/designer/config"},
	}...)

	return routes
}

func bench(base string, r route, n int) result {
	client := &http.Client{Timeout: 30 * time.Second}
	res := result{route: r, times: make([]time.Duration, 0, n)}

	for i := 0; i < n; i++ {
		start := time.Now()
		resp, err := client.Get(base + r.path)
		elapsed := time.Since(start)
		if err != nil {
			res.err = err
			return res
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		res.code = resp.StatusCode
		res.times = append(res.times, elapsed)
	}
	return res
}

// --- Output ---

func printGroupHeader(name string, hasDelta bool) {
	if hasDelta {
		fmt.Printf("\n  %-55s %6s %6s %6s %6s %6s %4s %7s\n",
			name, "Min", "Avg", "Med", "P95", "Max", "Code", "Δ Avg")
		fmt.Printf("  %-55s %6s %6s %6s %6s %6s %4s %7s\n",
			strings.Repeat("─", 55), "───", "───", "───", "───", "───", "────", "─────")
	} else {
		fmt.Printf("\n  %-55s %6s %6s %6s %6s %6s %4s\n",
			name, "Min", "Avg", "Med", "P95", "Max", "Code")
		fmt.Printf("  %-55s %6s %6s %6s %6s %6s %4s\n",
			strings.Repeat("─", 55), "───", "───", "───", "───", "───", "────")
	}
}

func printResult(r result, prev map[string]recordRoute) {
	if r.err != nil {
		fmt.Printf("  %-55s  ERROR: %v\n", r.route.path, r.err)
		return
	}
	s := calcStats(r.times)

	delta := ""
	if old, ok := prev[r.route.path]; ok {
		diff := s.avg - old.Avg
		if diff > 0 {
			delta = fmt.Sprintf("  +%dms", diff)
		} else if diff < 0 {
			delta = fmt.Sprintf("  %dms", diff)
		}
	}

	if len(prev) > 0 {
		fmt.Printf("  %-55s %5dms %5dms %5dms %5dms %5dms %4d %s\n",
			r.route.path, s.min, s.avg, s.med, s.p95, s.max, r.code, delta)
	} else {
		fmt.Printf("  %-55s %5dms %5dms %5dms %5dms %5dms %4d\n",
			r.route.path, s.min, s.avg, s.med, s.p95, s.max, r.code)
	}
}

type stats struct {
	min, avg, med, p95, max int64
}

func calcStats(times []time.Duration) stats {
	if len(times) == 0 {
		return stats{}
	}
	ms := make([]int64, len(times))
	var sum int64
	for i, t := range times {
		ms[i] = t.Milliseconds()
		sum += ms[i]
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })

	n := len(ms)
	med := ms[n/2]
	if n%2 == 0 {
		med = (ms[n/2-1] + ms[n/2]) / 2
	}
	p95idx := int(math.Ceil(float64(n)*0.95)) - 1
	if p95idx >= n {
		p95idx = n - 1
	}

	return stats{
		min: ms[0],
		avg: sum / int64(n),
		med: med,
		p95: ms[p95idx],
		max: ms[n-1],
	}
}

func printSummary(results []result) {
	var slow []result
	var errs []result
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r)
		} else if calcStats(r.times).avg > 50 {
			slow = append(slow, r)
		}
	}

	if len(errs) > 0 {
		fmt.Printf("  ERRORS (%d):\n", len(errs))
		for _, r := range errs {
			fmt.Printf("    %s — %v\n", r.route.path, r.err)
		}
		fmt.Println()
	}

	if len(slow) > 0 {
		fmt.Printf("  SLOW (>50ms avg):\n")
		for _, r := range slow {
			s := calcStats(r.times)
			fmt.Printf("    %s — avg %dms, p95 %dms\n", r.route.path, s.avg, s.p95)
		}
	} else if len(errs) == 0 {
		fmt.Println("  All routes under 50ms avg.")
	}
}

// --- Recording ---

func saveRecord(base string, n int, results []result) (string, error) {
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return "", err
	}

	now := time.Now()
	rec := record{
		Timestamp: now.Format(time.RFC3339),
		Base:      base,
		Runs:      n,
	}

	for _, r := range results {
		rr := recordRoute{
			Group: r.route.group,
			Path:  r.route.path,
			Code:  r.code,
		}
		if r.err != nil {
			rr.Error = r.err.Error()
		} else {
			s := calcStats(r.times)
			rr.Min = s.min
			rr.Avg = s.avg
			rr.Med = s.med
			rr.P95 = s.p95
			rr.Max = s.max
		}
		rec.Routes = append(rec.Routes, rr)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}

	filename := now.Format("2006-01-02T15-04-05") + ".json"
	path := filepath.Join(recordDir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func loadLatestRecord() *record {
	entries, err := os.ReadDir(recordDir)
	if err != nil {
		return nil
	}

	// Find the most recent .json file (names sort chronologically).
	var latest string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			latest = e.Name()
		}
	}
	if latest == "" {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(recordDir, latest))
	if err != nil {
		return nil
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	return &rec
}

// --- Discovery helpers: fetch real IDs from the running server ---

func discoverRouteIDs(base string) []string {
	body := fetchBody(base + "/api/transit/routes")
	if body == "" {
		return nil
	}
	var routes []struct {
		RouteID string `json:"route_id"`
	}
	if err := json.Unmarshal([]byte(body), &routes); err != nil {
		return nil
	}
	// Sample up to 3
	ids := make([]string, 0, 3)
	for i, r := range routes {
		if i >= 3 {
			break
		}
		ids = append(ids, r.RouteID)
	}
	return ids
}

func discoverMinutesID(base string) string {
	body := fetchBody(base + "/minutes")
	if body == "" {
		return ""
	}
	// Find first /minutes/NNN link
	for _, part := range strings.Split(body, `href="/minutes/`) {
		idx := strings.IndexByte(part, '"')
		if idx > 0 && idx < 10 {
			id := part[:idx]
			if isDigits(id) {
				return id
			}
		}
	}
	return ""
}

func fetchBody(url string) string {
	resp, err := http.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return string(b)
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
