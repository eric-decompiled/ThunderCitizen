package transit

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/transit/chunk"
)

// ChunkCache is the metrics read layer. It holds chunks in memory,
// lazy-fills from transit.route_band_chunk on miss, and exposes a small
// uniform API: One, Range, EarliestDate. Anything more specific (per
// route, per band, per day) is one filter() call away on the result.
//
// One SQL query feeds the cache:
//
//	SELECT * FROM transit.route_band_chunk
//	WHERE date BETWEEN $1 AND $2
//
// That's the entire read interface to the database. Aggregation across
// (route, time, band) is in-memory math via internal/transit/chunk; the
// cache never computes a number, it just hands back chunks.
//
// Lazy fill semantics:
//
//   - Each Range / One call asks ensureRange to load any dates in the
//     requested window that aren't in the cache yet (or have stale today
//     data). One SELECT covers the missing range.
//   - Today's chunks get a 30-second TTL so a re-seed or live rollup
//     becomes visible without bouncing the server. Every other day
//     caches forever — historical data is immutable.
//   - Days that returned zero rows are still marked "tried" so we don't
//     hammer the DB on every page load asking for empty days.
//
// What the cache does NOT do:
//
//   - It does NOT call BuildChunk. If today's row isn't in the rollup
//     table yet, today's cells render empty. The seeder and a future
//     periodic rollup are responsible for keeping the table populated;
//     that's a write-side concern.
//   - It does NOT compute aggregates. Every method returns
//     []chunk.ChunkView; the caller (handler, templ helper, JS frontend)
//     does the math.
type ChunkCache struct {
	db *pgxpool.Pool

	mu     sync.Mutex
	chunks map[chunkKey]chunk.ChunkView // primary store, keyed by chunk id
	loaded map[string]time.Time         // ISO date → when we last filled this day

	earliestQueried bool      // true after the first DB lookup
	earliestDate    time.Time // cached MIN(date) from route_band_chunk
}

// chunkKey is the (date, band, route_id) primary key. Date is a
// "YYYY-MM-DD" string for hashability and round-trip stability with the
// JSON wire format.
type chunkKey struct {
	Date    string
	Band    string
	RouteID string
}

// todayTTL is how long today's chunks stay cached before the next read
// refreshes them. Anything older than today is immutable and caches
// forever (until the process restarts).
const todayTTL = 30 * time.Second

// NewChunkCache wires a fresh cache to the database.
func NewChunkCache(db *pgxpool.Pool) *ChunkCache {
	return &ChunkCache{
		db:     db,
		chunks: map[chunkKey]chunk.ChunkView{},
		loaded: map[string]time.Time{},
	}
}

// One returns a single chunk by its (route, date, band) primary key.
// The second return is true when the chunk exists; false when it doesn't
// (e.g. a Sunday for a weekday-only route, or a date not yet rolled up).
func (c *ChunkCache) One(ctx context.Context, routeID string, date time.Time, band string) (chunk.ChunkView, bool, error) {
	day := DateOnly(date)
	if err := c.ensureRange(ctx, day, day); err != nil {
		return chunk.ChunkView{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.chunks[chunkKey{Date: day.Format("2006-01-02"), Band: band, RouteID: routeID}]
	return v, ok, nil
}

// Range returns every chunk whose date falls in [from, to] inclusive,
// sorted by (date, band, route_id). The caller filters in-memory for
// any narrower slice (one route, one band, one day).
func (c *ChunkCache) Range(ctx context.Context, from, to time.Time) ([]chunk.ChunkView, error) {
	from = DateOnly(from)
	to = DateOnly(to)
	if to.Before(from) {
		return nil, nil
	}
	if err := c.ensureRange(ctx, from, to); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fromISO := from.Format("2006-01-02")
	toISO := to.Format("2006-01-02")
	out := make([]chunk.ChunkView, 0, 64)
	for _, b := range c.chunks {
		if b.Date >= fromISO && b.Date <= toISO {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].Band != out[j].Band {
			return bandOrder(out[i].Band) < bandOrder(out[j].Band)
		}
		return out[i].RouteID < out[j].RouteID
	})
	return out, nil
}

// EarliestDate returns the earliest date the database has chunks for, or
// a zero time if the table is empty. Queried once from the DB on first
// call, then cached permanently — the earliest chunk date is immutable
// history that never changes. Used by the date selector to disable the
// prev arrow at the data boundary.
func (c *ChunkCache) EarliestDate(ctx context.Context) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.earliestQueried {
		c.earliestQueried = true
		var d time.Time
		err := c.db.QueryRow(ctx,
			`SELECT MIN(date) FROM transit.route_band_chunk`).Scan(&d)
		if err == nil && !d.IsZero() {
			c.earliestDate = d
		}
	}
	return c.earliestDate
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// ensureRange is the locking wrapper around fillLocked. Every public
// method calls it before reading the in-memory map.
func (c *ChunkCache) ensureRange(ctx context.Context, from, to time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fillLocked(ctx, from, to)
}

// fillLocked is the only place SQL touches the cache. It figures out
// which dates in [from, to] aren't loaded yet (or have stale today
// data), runs one SELECT covering the missing range, and stores the
// results in c.chunks. Caller must hold c.mu.
func (c *ChunkCache) fillLocked(ctx context.Context, from, to time.Time) error {
	todayISO := ServiceDate().Format("2006-01-02")
	now := time.Now()

	var missing []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		iso := d.Format("2006-01-02")
		loadedAt, ok := c.loaded[iso]
		if !ok {
			missing = append(missing, d)
			continue
		}
		// Today refreshes when stale. Historical days never refresh.
		if iso == todayISO && now.Sub(loadedAt) > todayTTL {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// One SELECT, one shape: WHERE date BETWEEN $1 AND $2.
	minDate := missing[0]
	maxDate := missing[0]
	for _, d := range missing[1:] {
		if d.Before(minDate) {
			minDate = d
		}
		if d.After(maxDate) {
			maxDate = d
		}
	}

	rows, err := c.db.Query(ctx, `
		SELECT route_id, date, band, service_kind,
		       trip_count, on_time_count,
		       scheduled_count, cancelled_count, no_notice_count,
		       headway_count, headway_sum_sec, headway_sum_sec_sq, sched_headway_sec,
		       built_at
		FROM transit.route_band_chunk
		WHERE date >= $1::date AND date <= $2::date
		ORDER BY date, route_id, band
	`, minDate, maxDate)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ck chunk.Chunk
		if err := rows.Scan(
			&ck.RouteID, &ck.Date, &ck.Band, &ck.ServiceKind,
			&ck.TripCount, &ck.OnTimeCount,
			&ck.ScheduledCount, &ck.CancelledCount, &ck.NoNoticeCount,
			&ck.HeadwayCount, &ck.HeadwaySumSec, &ck.HeadwaySumSecSq, &ck.SchedHeadwaySec,
			&ck.BuiltAt,
		); err != nil {
			return err
		}
		v := ck.View()
		c.chunks[chunkKey{Date: v.Date, Band: v.Band, RouteID: v.RouteID}] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Mark every requested date as "tried", even if it returned no rows.
	for _, d := range missing {
		c.loaded[d.Format("2006-01-02")] = now
	}
	return nil
}

// bandOrder maps a band name to its natural sort position. Used by
// Range so morning always comes before midday before evening,
// regardless of map iteration order.
func bandOrder(band string) int {
	switch band {
	case "morning":
		return 0
	case "midday":
		return 1
	case "evening":
		return 2
	}
	return 3
}
