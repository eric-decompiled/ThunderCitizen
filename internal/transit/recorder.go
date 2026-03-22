package transit

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/logger"
)

var recorderLog = logger.New("recorder")

const (
	// Gaps larger than this multiple of the interval get recorded.
	gapThreshold = 3
)

// Recorder polls GTFS-RT feeds via Client and writes events to Postgres.
//
// The recorder denormalizes trip context (route_id, pattern_id, service_id,
// service_kind, headsign, band, scheduled_first_dep_time) onto every
// transit.stop_delay and transit.cancellation row at write time, using the
// in-memory TripCache. Metric queries never need to re-derive context via
// joins — it's already on the observation row.
type Recorder struct {
	db      *pgxpool.Pool
	repo    *Repo
	client  *Client
	tracker *vehicleTracker
	trips   *TripCache
}

// NewRecorder creates a recorder with the given database pool. The trip cache
// is created here but not populated — the recorder's Start method will do
// the initial load after the loader has built Tier 2 entities.
func NewRecorder(db *pgxpool.Pool) *Recorder {
	return &Recorder{
		db:      db,
		repo:    NewRepo(db),
		client:  NewClient(),
		tracker: newVehicleTracker(db),
		trips:   NewTripCache(db),
	}
}

// Start launches background goroutines for each feed plus the trip cache
// refresh loop. The cache is initially loaded before the polling goroutines
// start so the first batch of delays/cancellations has enrichment data.
func (r *Recorder) Start(ctx context.Context) {
	recorderLog.Info("starting")

	// Initial load of the trip cache. If it fails we log and continue —
	// subsequent GTFS reload notifications will retry.
	if err := r.trips.Reload(ctx); err != nil {
		recorderLog.Warn("initial trip cache load failed", "err", err)
	} else {
		recorderLog.Info("trip cache loaded", "trips", r.trips.Size())
	}

	// Subscribe to GTFS reload notifications and reload the cache on each.
	go r.reloadLoop(ctx)

	go r.pollLoop(ctx, "vehicles", VehiclePollInterval, r.recordVehicles)
	go r.pollLoop(ctx, "trips", TripPollInterval, r.recordTrips)
	go r.pollLoop(ctx, "alerts", AlertPollInterval, r.recordAlerts)
}

// reloadLoop watches for GTFS reload notifications and rebuilds the trip
// cache on each. The loader fires NotifyGTFSReloaded after a successful
// Tier 2 build.
func (r *Recorder) reloadLoop(ctx context.Context) {
	ch := SubscribeGTFSReloaded()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			recorderLog.Info("reloading trip cache after GTFS reload")
			if err := r.trips.Reload(ctx); err != nil {
				recorderLog.Warn("trip cache reload failed", "err", err)
				continue
			}
			recorderLog.Info("trip cache reloaded", "trips", r.trips.Size())
		}
	}
}

type recordFunc func(ctx context.Context) (feedTS time.Time, err error)

func (r *Recorder) pollLoop(ctx context.Context, feedType string, interval time.Duration, record recordFunc) {
	recorderLog.Info("polling", "feed", feedType, "interval", interval)

	// Initial delay to let the server warm up
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}

	for {
		if err := r.fetchAndRecord(ctx, feedType, interval, record); err != nil {
			recorderLog.Error("poll failed", "feed", feedType, "err", err)
		}

		// Jitter: ±10% of interval
		jitter := time.Duration(rand.Int63n(int64(interval) / 5))
		jitter -= time.Duration(int64(interval) / 10)

		select {
		case <-time.After(interval + jitter):
		case <-ctx.Done():
			recorderLog.Info("stopped", "feed", feedType)
			return
		}
	}
}

func (r *Recorder) fetchAndRecord(ctx context.Context, feedType string, interval time.Duration, record recordFunc) error {
	feedTS, err := record(ctx)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	// Check for gaps
	if err := r.checkAndRecordGap(ctx, feedType, feedTS, interval); err != nil {
		recorderLog.Warn("gap check error", "feed", feedType, "err", err)
	}

	// Update feed state
	if err := r.repo.UpsertFeedState(ctx, feedType, feedTS); err != nil {
		return fmt.Errorf("update feed state: %w", err)
	}

	return nil
}

// recordVehicles fetches vehicle positions and persists them as events.
func (r *Recorder) recordVehicles(ctx context.Context) (time.Time, error) {
	feed, err := r.client.FetchVehicles(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetch: %w", err)
	}

	if err := r.insertVehiclePositions(ctx, feed.Positions); err != nil {
		recorderLog.Warn("insert positions", "err", err)
	}

	if err := r.tracker.processPositions(ctx, feed.Positions, feed.Timestamp); err != nil {
		recorderLog.Warn("tracker", "err", err)
	}

	return feed.Timestamp, nil
}

// recordTrips fetches trip updates and writes cancellations and per-stop
// delays as events.
func (r *Recorder) recordTrips(ctx context.Context) (time.Time, error) {
	feed, err := r.client.FetchTrips(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetch: %w", err)
	}

	if len(feed.Cancellations) > 0 {
		if err := r.insertTripCancellations(ctx, feed.Cancellations); err != nil {
			return time.Time{}, err
		}
	}

	if len(feed.Delays) > 0 {
		if err := r.upsertTripStopActuals(ctx, feed.Delays); err != nil {
			recorderLog.Warn("actuals upsert", "err", err)
		}
	}

	return feed.Timestamp, nil
}

// recordAlerts fetches service alerts and writes them as events.
func (r *Recorder) recordAlerts(ctx context.Context) (time.Time, error) {
	feed, err := r.client.FetchAlerts(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetch: %w", err)
	}

	if len(feed.Observations) > 0 {
		if err := r.insertAlertObservations(ctx, feed.Observations); err != nil {
			return time.Time{}, err
		}
	}

	return feed.Timestamp, nil
}

// --- batch inserts ---

func (r *Recorder) insertVehiclePositions(ctx context.Context, positions []VehiclePosition) error {
	if len(positions) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range positions {
		p := &positions[i]
		if p.VehicleID == "" {
			continue
		}
		batch.Queue(
			`INSERT INTO transit.vehicle_position (feed_timestamp, vehicle_id, route_id, trip_id, latitude, longitude, bearing, speed, stop_status, current_stop_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.FeedTimestamp, p.VehicleID, p.RouteID, p.TripID,
			p.Latitude, p.Longitude, p.Bearing, p.Speed,
			p.StopStatus, p.CurrentStopID,
		)
	}
	if batch.Len() == 0 {
		return nil
	}
	return r.execBatch(ctx, batch, batch.Len(), "vehicle position")
}

// upsertTripStopActuals writes per-stop delays to transit.stop_delay with
// trip context denormalized from the in-memory trip cache. Delays whose
// trip_id is not in the cache (orphans from a feed transition) are logged
// and dropped — a row without context is worse than no row.
//
// is_timepoint requires knowing whether this particular stop is a timepoint
// on the trip's pattern. We can't look that up cheaply from the cache (it's
// keyed by trip_id, not trip_id+stop_id). For now we leave is_timepoint
// false and let the metric queries join against transit.route_pattern_stop
// when they need timepoint-only filtering.
func (r *Recorder) upsertTripStopActuals(ctx context.Context, delays []DelayObservation) error {
	if len(delays) == 0 {
		return nil
	}
	svcDate := ServiceDate()

	var dropped int
	batch := &pgx.Batch{}
	for _, d := range delays {
		ti, ok := r.trips.Lookup(d.TripID)
		if !ok {
			dropped++
			continue
		}
		var seq *int32
		if d.StopSequence != nil {
			seq = d.StopSequence
		}
		isFirstStop := false
		if seq != nil && *seq == 1 {
			isFirstStop = true
		}
		batch.Queue(
			`INSERT INTO transit.stop_delay AS sd (
				date, trip_id, stop_id, stop_sequence,
				arrival_delay, departure_delay, last_updated,
				route_id, pattern_id, service_id, service_kind,
				headsign, band, is_first_stop, is_timepoint,
				scheduled_first_dep_time
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, NOW(),
				$7, $8, $9, $10,
				$11, $12, $13, false,
				$14
			)
			ON CONFLICT (date, trip_id, stop_id) DO UPDATE SET
				arrival_delay   = COALESCE(EXCLUDED.arrival_delay, sd.arrival_delay),
				departure_delay = COALESCE(EXCLUDED.departure_delay, sd.departure_delay),
				last_updated    = NOW()`,
			svcDate, d.TripID, d.StopID, seq,
			d.ArrivalDelay, d.DepartureDelay,
			ti.RouteID, ti.PatternID, ti.ServiceID, ti.ServiceKind,
			ti.Headsign, ti.Band, isFirstStop,
			ti.ScheduledFirstDepTime,
		)
	}

	if dropped > 0 {
		recorderLog.Warn("dropped delays: trip not in catalog", "count", dropped, "cache_size", r.trips.Size())
	}
	if batch.Len() == 0 {
		return nil
	}
	return r.execBatch(ctx, batch, batch.Len(), "trip stop actual")
}

// insertTripCancellations writes cancellations with denormalized headsign
// + scheduled_last_arr_time + pattern_id from the trip cache when available.
// Orphan cancellations (trip_id not in cache) still get recorded — the
// cancellation itself is the important signal — just with empty denorm fields.
func (r *Recorder) insertTripCancellations(ctx context.Context, cancellations []TripCancellation) error {
	if len(cancellations) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range cancellations {
		var headsign, patternID, lastArr string
		if ti, ok := r.trips.Lookup(c.TripID); ok {
			headsign = ti.Headsign
			patternID = ti.PatternID
			lastArr = ti.ScheduledLastArrTime
		}
		batch.Queue(
			`INSERT INTO transit.cancellation (
				feed_timestamp, trip_id, route_id, start_date, start_time,
				schedule_relationship, headsign, pattern_id, scheduled_last_arr_time
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 ON CONFLICT (trip_id, feed_timestamp) DO NOTHING`,
			c.FeedTimestamp, c.TripID, c.RouteID, c.StartDate, c.StartTime,
			c.ScheduleRelationship, headsign, patternID, lastArr,
		)
	}
	return r.execBatch(ctx, batch, len(cancellations), "trip cancellation")
}

func (r *Recorder) insertAlertObservations(ctx context.Context, observations []AlertObservation) error {
	batch := &pgx.Batch{}
	for _, o := range observations {
		batch.Queue(
			`INSERT INTO transit.alert (feed_timestamp, alert_id, cause, effect, header, description, severity_level, url, active_start, active_end, affected_routes, affected_stops)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			 ON CONFLICT (alert_id, feed_timestamp) DO NOTHING`,
			o.FeedTimestamp, o.AlertID, o.Cause, o.Effect, o.Header, o.Description,
			o.SeverityLevel, o.URL, o.ActiveStart, o.ActiveEnd,
			o.AffectedRoutes, o.AffectedStops,
		)
	}
	return r.execBatch(ctx, batch, len(observations), "alert observation")
}

func (r *Recorder) execBatch(ctx context.Context, batch *pgx.Batch, count int, label string) error {
	br := r.db.SendBatch(ctx, batch)
	defer br.Close()

	for range count {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch insert %s: %w", label, err)
		}
	}
	return nil
}

// --- feed state tracking ---

func (r *Recorder) checkAndRecordGap(ctx context.Context, feedType string, feedTS time.Time, expectedInterval time.Duration) error {
	lastTS, err := r.repo.GetFeedLastTimestamp(ctx, feedType)
	if err != nil {
		return nil // first run, no gap
	}

	gap := feedTS.Sub(lastTS)
	threshold := time.Duration(gapThreshold) * expectedInterval

	if gap > threshold {
		err := r.repo.InsertFeedGap(ctx, feedType, lastTS, feedTS, int32(expectedInterval.Seconds()), int32(gap.Seconds()))
		if err != nil {
			return err
		}
		recorderLog.Warn("gap detected", "feed", feedType, "from", lastTS, "to", feedTS, "duration", gap)
	}

	return nil
}
