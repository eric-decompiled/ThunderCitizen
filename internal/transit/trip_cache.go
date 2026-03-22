package transit

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trip_cache.go is the recorder's in-memory snapshot of transit.trip_catalog.
// At write time, the recorder enriches each transit.stop_delay row with the
// trip's route/pattern/service/headsign/band/scheduled_first_dep_time so the
// row is fully self-describing. The cache is a map[trip_id]TripIdentity held
// behind an atomic.Pointer for lock-free reads.
//
// The cache is rebuilt on:
//   - Recorder startup (initial load).
//   - GTFS reload notifications (the loader fires NotifyGTFSReloaded after a
//     successful Tier 2 build, and every subscriber wakes up).
//
// If a trip_id arrives in the GTFS-RT feed that's not in the cache (orphan
// from a feed transition), the recorder logs and skips. Better to drop one
// observation than to insert a context-less row.

// TripIdentity is the per-trip context the recorder needs to denormalize
// stop_delay rows.
type TripIdentity struct {
	RouteID               string
	PatternID             string
	ServiceID             string
	ServiceKind           string // weekday | saturday | sunday
	Headsign              string
	DirectionID           int
	Band                  string // morning | midday | evening
	ScheduledFirstDepTime string // HH:MM:SS
	ScheduledLastArrTime  string // HH:MM:SS
}

// TripCache is a lock-free in-memory snapshot of transit.trip_catalog.
type TripCache struct {
	db   *pgxpool.Pool
	data atomic.Pointer[map[string]TripIdentity]
}

// NewTripCache creates an empty trip cache. Call Reload to populate it.
func NewTripCache(db *pgxpool.Pool) *TripCache {
	c := &TripCache{db: db}
	empty := make(map[string]TripIdentity)
	c.data.Store(&empty)
	return c
}

// Reload reads transit.trip_catalog into a fresh map and atomically swaps
// it in. Existing readers see the previous map until the swap is visible.
func (c *TripCache) Reload(ctx context.Context) error {
	rows, err := c.db.Query(ctx, `
		SELECT
			trip_id, route_id, pattern_id, service_id, service_kind,
			headsign, direction_id, band,
			scheduled_first_dep_time, scheduled_last_arr_time
		FROM transit.trip_catalog
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	next := make(map[string]TripIdentity, 4096)
	for rows.Next() {
		var tripID string
		var ti TripIdentity
		if err := rows.Scan(
			&tripID, &ti.RouteID, &ti.PatternID, &ti.ServiceID, &ti.ServiceKind,
			&ti.Headsign, &ti.DirectionID, &ti.Band,
			&ti.ScheduledFirstDepTime, &ti.ScheduledLastArrTime,
		); err != nil {
			return err
		}
		next[tripID] = ti
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.data.Store(&next)
	return nil
}

// Lookup returns the TripIdentity for a given trip_id, or false if not found.
// Lock-free — safe to call from any goroutine.
func (c *TripCache) Lookup(tripID string) (TripIdentity, bool) {
	m := c.data.Load()
	if m == nil {
		return TripIdentity{}, false
	}
	ti, ok := (*m)[tripID]
	return ti, ok
}

// Size returns the current number of cached trips.
func (c *TripCache) Size() int {
	m := c.data.Load()
	if m == nil {
		return 0
	}
	return len(*m)
}

// --- GTFS reload notification ---
//
// A simple pub/sub for "Tier 2 has just been rebuilt by the loader." The
// loader calls NotifyGTFSReloaded() after a successful DeriveTier2; every
// subscriber's channel receives a non-blocking signal. Subscribers (the
// recorder's trip cache, the metrics cache invalidator) listen on their
// channel in a goroutine and rebuild their state.

var (
	reloadMu          sync.Mutex
	reloadSubscribers []chan struct{}
)

// SubscribeGTFSReloaded registers a channel that receives a non-blocking
// signal each time DeriveTier2 completes successfully. The channel is
// buffered (size 1) so callers can't miss back-to-back reloads.
func SubscribeGTFSReloaded() <-chan struct{} {
	ch := make(chan struct{}, 1)
	reloadMu.Lock()
	reloadSubscribers = append(reloadSubscribers, ch)
	reloadMu.Unlock()
	return ch
}

// NotifyGTFSReloaded fires the reload signal to every subscriber. Non-blocking
// — if a subscriber's buffer is full, the signal is dropped (it'll see the
// next one).
func NotifyGTFSReloaded() {
	reloadMu.Lock()
	subs := append([]chan struct{}(nil), reloadSubscribers...)
	reloadMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
