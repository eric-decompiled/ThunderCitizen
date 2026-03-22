// Package main is the seedtransit dev tool.
//
// Generates synthetic chunks for the dev database. One chunk per
// (route, date, band) — 6 hours of one route's stats. Stats are
// computed in Go (synthesize), then upserted directly into
// transit.route_band_chunk. The seeder doesn't call BuildChunk; it
// produces pre-computed chunk values and inserts them row by row.
//
// Routes get beer-style display names (see fallbackRoutes) so it's
// obvious in the grid that you're looking at synthetic dev data, not
// real Thunder Bay transit performance.
package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// band mirrors internal/transit.Band — duplicated locally so this dev tool
// doesn't import the transit package. The three windows must match the
// canonical Bands list exactly. Together they stack into one layer.
type band struct {
	Name      string
	StartHour int
	EndHour   int
}

// the three bands stacked into one layer, served in this order.
var bands = []band{
	{"morning", 6, 12},
	{"midday", 12, 18},
	{"evening", 18, 24},
}

// fallbackRoutes is the brewery — 20 beer styles inserted when
// transit.route is empty (fresh DB without GTFS loaded). The style names
// make it obvious in the grid that you're looking at synthetic dev data,
// not the real Thunder Bay route map. Colors run light-to-dark down the
// list, roughly tracking the beers themselves: golden pilsners up top,
// amber pale ales in the middle, jet-black imperial stouts near the
// bottom, with one ruby sour for variety. Load real GTFS via
// `./bin/fetcher gtfs` to replace the brewery with actual routes.
var fallbackRoutes = []struct {
	ID, ShortName, DisplayName, Color string
}{
	{"1", "1", "Pilsner", "#f5c842"},
	{"2", "2", "Helles Lager", "#e8b231"},
	{"3", "3", "Kölsch", "#efd075"},
	{"4", "4", "Witbier", "#e8d97f"},
	{"5", "5", "Hefeweizen", "#d8b365"},
	{"6", "6", "Saison", "#d4a73a"},
	{"7", "7", "Pale Ale", "#d4853e"},
	{"8", "8", "Hazy IPA", "#e09146"},
	{"9", "9", "West Coast IPA", "#cd6e1f"},
	{"10", "10", "Tripel", "#c9912e"},
	{"11", "11", "Amber Ale", "#a05a2c"},
	{"12", "12", "Vienna Lager", "#9b5121"},
	{"13", "13", "Brown Ale", "#6b3410"},
	{"14", "14", "Bock", "#663211"},
	{"15", "15", "Doppelbock", "#4d2208"},
	{"16", "16", "Porter", "#2b1810"},
	{"17", "17", "Stout", "#1a0f08"},
	{"18", "18", "Imperial Stout", "#0a0506"},
	{"19", "19", "Sour", "#b8455a"},
	{"20", "20", "Barleywine", "#58200a"},
}

// Seeder owns the PRNG and DB handle for one seeding run.
type Seeder struct {
	db  *pgxpool.Pool
	rng *rand.Rand
}

// Summary is the result of a seeding run, printed at the end.
type Summary struct {
	Routes        int
	Chunks        int
	Cancellations int
}

func NewSeeder(db *pgxpool.Pool, seed int64) *Seeder {
	return &Seeder{db: db, rng: rand.New(rand.NewSource(seed))}
}

// Clean removes previously-seeded chunk rows in [from, to] inclusive
// and any synthetic cancellation rows whose trip_id starts with "seed_".
// Returns the number of chunk rows deleted.
func (s *Seeder) Clean(ctx context.Context, from, to time.Time) (int64, error) {
	res, err := s.db.Exec(ctx, `
		DELETE FROM transit.route_band_chunk
		WHERE date >= $1::date AND date <= $2::date
	`, from, to)
	if err != nil {
		return 0, err
	}
	chunkRows := res.RowsAffected()

	if _, err := s.db.Exec(ctx, `
		DELETE FROM transit.cancellation
		WHERE trip_id LIKE 'seed_%'
		  AND feed_timestamp >= $1::date
		  AND feed_timestamp < ($2::date + 2)
	`, from, to); err != nil {
		return chunkRows, err
	}
	return chunkRows, nil
}

// Run seeds chunks and per-trip cancellation detail for [from, to]
// inclusive. The PRNG state is consumed in (date, route, band) order, so
// the same seed always produces the same output for a given range.
func (s *Seeder) Run(ctx context.Context, from, to time.Time) (Summary, error) {
	var sum Summary

	routes, err := s.ensureRoutes(ctx)
	if err != nil {
		return sum, err
	}
	sum.Routes = len(routes)

	days := int(to.Sub(from).Hours()/24) + 1

	for di := 0; di < days; di++ {
		date := from.AddDate(0, 0, di)
		// progress: 0 at the oldest day (worst service), 1 at the newest
		// (best service). One-day runs collapse to progress=1.
		progress := 1.0
		if days > 1 {
			progress = float64(di) / float64(days-1)
		}
		kind := serviceKind(date)

		for _, routeID := range routes {
			for _, b := range bands {
				ck := s.synthesize(routeID, date, kind, b, progress)
				if err := s.upsertChunk(ctx, ck); err != nil {
					return sum, fmt.Errorf("upsert %s/%s/%s: %w", routeID, date.Format("2006-01-02"), b.Name, err)
				}
				sum.Chunks++

				if ck.CancelledCount > 0 {
					n, err := s.insertCancellations(ctx, ck, b)
					if err != nil {
						return sum, fmt.Errorf("cancels %s/%s/%s: %w", routeID, date.Format("2006-01-02"), b.Name, err)
					}
					sum.Cancellations += n
				}
			}
		}
	}
	return sum, nil
}

// ensureRoutes returns the route ID list. Inserts fallbackRoutes only if
// transit.route is completely empty — never overwrites existing GTFS data.
func (s *Seeder) ensureRoutes(ctx context.Context) ([]string, error) {
	var n int
	if err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM transit.route").Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		fmt.Println("  brewery is empty — pouring 20 beer-style routes into transit.route")
		for _, r := range fallbackRoutes {
			_, err := s.db.Exec(ctx, `
				INSERT INTO transit.route (route_id, short_name, long_name, display_name, color, text_color, sort_order)
				VALUES ($1, $2, $3, $4, $5, '#FFFFFF', $6)
				ON CONFLICT DO NOTHING
			`, r.ID, r.ShortName, r.DisplayName, r.DisplayName, r.Color, parseRouteSortOrder(r.ID))
			if err != nil {
				return nil, fmt.Errorf("insert fallback route %s: %w", r.ID, err)
			}
		}
	}

	rows, err := s.db.Query(ctx, "SELECT route_id FROM transit.route ORDER BY sort_order, route_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no routes found in transit.route after fallback insert")
	}
	return out, nil
}

// chunk is the local row shape for transit.route_band_chunk. Fields
// match the table columns 1:1.
type chunk struct {
	RouteID         string
	Date            time.Time
	Band            string
	ServiceKind     string
	TripCount       int
	OnTimeCount     int
	ScheduledCount  int
	CancelledCount  int
	NoNoticeCount   int
	HeadwayCount    int
	HeadwaySumSec   float64
	HeadwaySumSecSq float64
	SchedHeadwaySec float64
}

func (s *Seeder) upsertChunk(ctx context.Context, b chunk) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO transit.route_band_chunk (
			route_id, date, band, service_kind,
			trip_count, on_time_count,
			scheduled_count, cancelled_count, no_notice_count,
			headway_count, headway_sum_sec, headway_sum_sec_sq, sched_headway_sec,
			built_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
		ON CONFLICT (route_id, date, band) DO UPDATE SET
			service_kind = EXCLUDED.service_kind,
			trip_count = EXCLUDED.trip_count,
			on_time_count = EXCLUDED.on_time_count,
			scheduled_count = EXCLUDED.scheduled_count,
			cancelled_count = EXCLUDED.cancelled_count,
			no_notice_count = EXCLUDED.no_notice_count,
			headway_count = EXCLUDED.headway_count,
			headway_sum_sec = EXCLUDED.headway_sum_sec,
			headway_sum_sec_sq = EXCLUDED.headway_sum_sec_sq,
			sched_headway_sec = EXCLUDED.sched_headway_sec,
			built_at = now()
	`,
		b.RouteID, b.Date, b.Band, b.ServiceKind,
		b.TripCount, b.OnTimeCount,
		b.ScheduledCount, b.CancelledCount, b.NoNoticeCount,
		b.HeadwayCount, b.HeadwaySumSec, b.HeadwaySumSecSq, b.SchedHeadwaySec,
	)
	return err
}

// insertCancellations writes one transit.cancellation row per cancelled
// trip in the chunk, with realistic-looking departure times distributed
// across the band hours and lead-time skewed so the no-notice fraction
// matches chunk.NoNoticeCount.
func (s *Seeder) insertCancellations(ctx context.Context, b chunk, bnd band) (int, error) {
	bandHours := float64(bnd.EndHour - bnd.StartHour)
	headsigns := []string{"Inbound", "Outbound", "Memorial", "Crosstown", "Mainline"}
	inserted := 0

	for i := 0; i < b.CancelledCount; i++ {
		// Random scheduled departure within the band's hour range.
		hourOffset := s.rng.Float64() * bandHours
		depHourF := float64(bnd.StartHour) + hourOffset
		h := int(depHourF)
		m := int((depHourF - float64(h)) * 60)
		startTime := fmt.Sprintf("%02d:%02d:00", h, m)

		// Lead time: the first NoNoticeCount cancellations get short notice
		// (< 15 min), the rest get reasonable notice (15-75 min).
		var leadMin float64
		if i < b.NoNoticeCount {
			leadMin = s.rng.Float64() * 14
		} else {
			leadMin = 15 + s.rng.Float64()*60
		}
		scheduled := time.Date(b.Date.Year(), b.Date.Month(), b.Date.Day(), h, m, 0, 0, time.UTC)
		feedTS := scheduled.Add(-time.Duration(leadMin*60) * time.Second)

		tripID := fmt.Sprintf("seed_%s_%s_%s_%d",
			b.RouteID, b.Date.Format("20060102"), bnd.Name, i)
		headsign := headsigns[s.rng.Intn(len(headsigns))]

		_, err := s.db.Exec(ctx, `
			INSERT INTO transit.cancellation (
				feed_timestamp, trip_id, route_id, start_date, start_time,
				schedule_relationship, headsign, pattern_id, scheduled_last_arr_time
			) VALUES ($1, $2, $3, $4, $5, 'CANCELED', $6, '', '')
		`, feedTS, tripID, b.RouteID, b.Date.Format("20060102"), startTime, headsign)
		if err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

// synthesize returns one realistic-looking chunk. progress is 0 at the
// oldest day (worst) and 1 at the newest (best); the function interpolates
// targets between the two extremes, applies a per-route quality bias so
// some routes are persistently good performers and others are persistently
// bad, then adds seeded gaussian noise so consecutive days don't look
// perfectly straight.
//
// EWT and Cv are mathematically linked: AWT = (mean_h/2) * (1 + Cv²) and
// EWT = AWT - sched/2 = (mean_h/2) * Cv² when sched_h equals the observed
// mean. The seeder picks a target Cv and back-derives the headway sums so
// the chunk math in internal/transit/chunk/math.go reproduces the same
// number from the same row. EWT then falls out for free.
func (s *Seeder) synthesize(routeID string, date time.Time, kind string, b band, progress float64) chunk {
	// Targets at the extremes — chosen to give a visible "bad → good"
	// trend across the date range.
	const (
		otpWorst    = 55.0 // %
		otpBest     = 92.0
		cancelWorst = 6.0 // % of scheduled trips
		cancelBest  = 0.5
		cvWorst     = 0.55 // dimensionless
		cvBest      = 0.15
	)

	// Per-route quality bias in [-1, +1]. Positive = consistently good
	// performer (high OTP, low cancel, low Cv); negative = consistently
	// bad. Hash-based and deterministic, so the same route_id always has
	// the same bias regardless of seed — letting the user identify
	// "the route that's always struggling" across runs.
	q := routeQuality(routeID)

	otpTarget := lerp(otpWorst, otpBest, progress) + q*15.0 + s.gauss()*3.0
	cancelTarget := lerp(cancelWorst, cancelBest, progress) - q*2.5 + s.gauss()*0.4
	cvTarget := lerp(cvWorst, cvBest, progress) - q*0.15 + s.gauss()*0.05
	otpTarget = clamp(otpTarget, 0, 100)
	cancelTarget = clamp(cancelTarget, 0, 100)
	cvTarget = clamp(cvTarget, 0.05, 1.5)

	// Trip volume — ~18 trips/band/route on weekdays. Weekends and
	// midday are slightly lighter.
	base := 18.0
	switch kind {
	case "saturday":
		base *= 0.75
	case "sunday":
		base *= 0.55
	}
	if b.Name == "midday" {
		base *= 0.85
	}
	trips := int(math.Round(math.Max(1, base+s.gauss()*4)))
	onTime := int(math.Round(float64(trips) * otpTarget / 100))
	if onTime > trips {
		onTime = trips
	}

	// Scheduled count is slightly above trips so cancelled trips that
	// never produced an observation still count toward the denominator.
	scheduled := trips + s.rng.Intn(3)
	cancelled := int(math.Round(float64(scheduled) * cancelTarget / 100))
	if cancelled > scheduled {
		cancelled = scheduled
	}
	noNotice := int(math.Round(float64(cancelled) * 0.4))
	if noNotice > cancelled {
		noNotice = cancelled
	}

	// Headway sums chosen to satisfy the target Cv exactly:
	//   var(h) = (Cv * mean)^2
	//   sum(h)   = N * mean
	//   sum(h^2) = N * (var + mean^2) = N * mean^2 * (1 + Cv^2)
	// EWT then derives from the same numbers via chunk.EWTSec(...).
	meanH := 1800.0 // 30 min — typical Thunder Bay headway in seconds
	headwayN := trips * 3
	if headwayN < 2 {
		headwayN = 2
	}
	sumH := float64(headwayN) * meanH
	sumHSq := float64(headwayN) * meanH * meanH * (1 + cvTarget*cvTarget)

	return chunk{
		RouteID:         routeID,
		Date:            date,
		Band:            b.Name,
		ServiceKind:     kind,
		TripCount:       trips,
		OnTimeCount:     onTime,
		ScheduledCount:  scheduled,
		CancelledCount:  cancelled,
		NoNoticeCount:   noNotice,
		HeadwayCount:    headwayN,
		HeadwaySumSec:   sumH,
		HeadwaySumSecSq: sumHSq,
		SchedHeadwaySec: meanH,
	}
}

// gauss returns a Box-Muller normal sample, mean 0 stddev 1.
func (s *Seeder) gauss() float64 {
	u1 := s.rng.Float64()
	u2 := s.rng.Float64()
	if u1 == 0 {
		u1 = 1e-12
	}
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func serviceKind(date time.Time) string {
	switch date.Weekday() {
	case time.Saturday:
		return "saturday"
	case time.Sunday:
		return "sunday"
	default:
		return "weekday"
	}
}

func parseRouteSortOrder(id string) int {
	var n int
	fmt.Sscanf(id, "%d", &n)
	return n
}

// routeQuality returns a deterministic per-route bias in [-1, +1] derived
// from a hash of the route_id. Positive routes are persistently good
// performers; negative routes are persistently bad. The distribution is
// triangular (sum of two uniforms) so most routes cluster near the middle
// and only a few sit at the extremes — same shape as a real transit
// agency where 80% of routes are average and a handful are problem
// children.
//
// Hash-based and seed-independent: the same route_id always gets the same
// quality, so "the route that's always struggling" stays consistent
// across reseeds. Different routes can be told apart by eye in the grid.
func routeQuality(routeID string) float64 {
	// FNV-1a 64-bit
	var h uint64 = 14695981039346656037
	for i := 0; i < len(routeID); i++ {
		h ^= uint64(routeID[i])
		h *= 1099511628211
	}
	// Two independent uniform draws from the hash, summed → triangular.
	a := float64(h%1000) / 999.0
	b := float64((h>>20)%1000) / 999.0
	return (a + b) - 1.0
}
