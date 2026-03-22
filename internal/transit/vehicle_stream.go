package transit

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/logger"
)

var streamLog = logger.New("vehicle-stream")

// sleepPayload is sent to SSE clients when service is not running (1–6 AM).
var sleepPayload = json.RawMessage(`{"sleep":true}`)

// VehicleStream polls the upstream feeds and broadcasts vehicle state to
// connected SSE clients. One upstream fetch serves all clients.
type VehicleStream struct {
	client   *Client
	db       *pgxpool.Pool
	interval time.Duration

	mu         sync.RWMutex
	current    []byte // latest JSON payload (pre-serialized)
	currentRaw []byte // latest raw protobuf from upstream
	ts         int64  // feed timestamp

	// Subscribers: each is a channel that receives new payloads
	subMu sync.Mutex
	subs  map[chan []byte]struct{}

	// In-memory stop cache for nearest-stop lookups
	stops      []cachedStop
	routeStops map[string][]string // routeID → stop IDs on that route

	// Stop-visit detection: previous positions and last-served times
	prevPos     map[string]prevPosition // vehicleID → last position
	lastServed  map[string]int64        // stopID → unix timestamp
	lastServedM sync.RWMutex
}

type cachedStop struct {
	id   string
	name string
	lat  float64
	lon  float64
}

type prevPosition struct {
	lat, lon float64
	routeID  string
}

// NewVehicleStream creates a broadcaster that polls at the given interval.
func NewVehicleStream(client *Client, db *pgxpool.Pool, interval time.Duration) *VehicleStream {
	return &VehicleStream{
		client:     client,
		db:         db,
		interval:   interval,
		subs:       make(map[chan []byte]struct{}),
		prevPos:    make(map[string]prevPosition),
		lastServed: make(map[string]int64),
	}
}

// Start begins polling in the background. Cancel the context to stop.
// During overnight quiet hours (3–5 AM) polling pauses entirely since
// no buses are running and there's no point hitting the upstream feed.
func (vs *VehicleStream) Start(ctx context.Context) {
	streamLog.Info("starting", "interval", vs.interval)

	// Load stops into memory for nearest-stop lookups
	vs.loadStops(ctx)

	// Initial fetch (unless quiet hours)
	if !serviceQuiet() {
		vs.poll(ctx)
	} else {
		vs.mu.Lock()
		vs.current = sleepPayload
		vs.currentRaw = nil
		vs.mu.Unlock()
	}

	go func() {
		ticker := time.NewTicker(vs.interval)
		defer ticker.Stop()
		sleeping := false
		for {
			select {
			case <-ticker.C:
				if serviceQuiet() {
					if !sleeping {
						streamLog.Info("entering sleep — no service running")
						sleeping = true
						vs.mu.Lock()
						vs.current = sleepPayload
						vs.currentRaw = nil
						vs.mu.Unlock()
						vs.broadcast(sleepPayload)
					}
					continue
				}
				if sleeping {
					streamLog.Info("waking up — service window starting")
					sleeping = false
				}
				vs.poll(ctx)
			case <-ctx.Done():
				streamLog.Info("stopped")
				return
			}
		}
	}()
}

func (vs *VehicleStream) loadStops(ctx context.Context) {
	repo := NewRepo(vs.db)
	stops, err := repo.AllStops(ctx)
	if err != nil {
		streamLog.Error("failed to load stops", "err", err)
		return
	}
	vs.stops = make([]cachedStop, len(stops))
	for i, s := range stops {
		vs.stops[i] = cachedStop{id: s.StopID, name: s.StopName, lat: s.Latitude, lon: s.Longitude}
	}
	streamLog.Info("loaded stops", "count", len(vs.stops))

	// Load route → stop mapping from the stable pattern tables. Never joins
	// through gtfs.* staging — the route patterns are Tier 2 entities that
	// survive feed churn.
	rows, err := vs.db.Query(ctx, `
		SELECT DISTINCT rp.route_id, rps.stop_id
		FROM transit.route_pattern rp
		JOIN transit.route_pattern_stop rps USING (pattern_id)
		ORDER BY rp.route_id, rps.stop_id`)
	if err != nil {
		streamLog.Error("failed to load route stops", "err", err)
		return
	}
	defer rows.Close()

	vs.routeStops = make(map[string][]string, 20)
	for rows.Next() {
		var routeID, stopID string
		if err := rows.Scan(&routeID, &stopID); err != nil {
			streamLog.Error("scanning route stop", "err", err)
			return
		}
		vs.routeStops[routeID] = append(vs.routeStops[routeID], stopID)
	}

	// Seed lastServed from today's stop_visits
	served, err := repo.StopLastServed(ctx)
	if err != nil {
		streamLog.Warn("seed last-served", "err", err)
	} else {
		vs.lastServedM.Lock()
		for sid, t := range served {
			vs.lastServed[sid] = t.Unix()
		}
		vs.lastServedM.Unlock()
	}
}

func (vs *VehicleStream) enrichNearStop(vehicles []VehicleWithDelay) {
	// Build stop ID → name map for STOPPED_AT / INCOMING_AT
	stopNames := make(map[string]string, len(vs.stops))
	for _, s := range vs.stops {
		stopNames[s.id] = s.name
	}

	for i := range vehicles {
		v := &vehicles[i]
		if v.StopID != "" {
			if name, ok := stopNames[v.StopID]; ok {
				v.NearStop = name
				continue
			}
		}
		// IN_TRANSIT_TO or no stop ID: find nearest stop by distance
		if len(vs.stops) == 0 {
			continue
		}
		bestDist := math.MaxFloat64
		bestName := ""
		for _, s := range vs.stops {
			dlat := v.Lat - s.lat
			dlon := v.Lon - s.lon
			d := dlat*dlat + dlon*dlon // no need for sqrt, just comparing
			if d < bestDist {
				bestDist = d
				bestName = s.name
			}
		}
		v.NearStop = bestName
	}
}

// serviceQuiet returns true during overnight hours when no buses run.
// Thunder Bay service runs 06:00–24:48 (GTFS time). The 1 AM–6 AM
// window has zero service, so we skip polling the upstream feed entirely.
func serviceQuiet() bool {
	h := Now().Hour()
	return h >= 1 && h < 6
}

func (vs *VehicleStream) poll(ctx context.Context) {
	vehicles, feedTS, err := vs.client.FetchVehiclesWithDelay(ctx)
	if err != nil {
		streamLog.Error("fetch failed", "err", err)
		return
	}

	// Cache raw protobuf alongside parsed data
	if raw, err := vs.client.FetchVehiclesRaw(ctx); err == nil {
		vs.mu.Lock()
		vs.currentRaw = raw
		vs.mu.Unlock()
	}

	tsUnix := feedTS.Unix()

	vs.mu.RLock()
	same := vs.ts == tsUnix
	vs.mu.RUnlock()
	if same {
		return // no new data
	}

	// Enrich with nearest stop name
	vs.enrichNearStop(vehicles)

	// Detect stop visits via segment interpolation
	vs.detectStopVisits(vehicles, feedTS)

	// Build stop_last_served snapshot
	vs.lastServedM.RLock()
	servedCopy := make(map[string]int64, len(vs.lastServed))
	for k, v := range vs.lastServed {
		servedCopy[k] = v
	}
	vs.lastServedM.RUnlock()

	payload, err := json.Marshal(map[string]interface{}{
		"timestamp":   tsUnix,
		"vehicles":    vehicles,
		"stop_served": servedCopy,
	})
	if err != nil {
		return
	}

	vs.mu.Lock()
	vs.current = payload
	vs.ts = tsUnix
	vs.mu.Unlock()

	vs.broadcast(payload)
}

// broadcast sends a payload to all connected SSE subscribers (non-blocking).
func (vs *VehicleStream) broadcast(payload []byte) {
	vs.subMu.Lock()
	for ch := range vs.subs {
		select {
		case ch <- payload:
		default:
			// Slow client — drop this update
		}
	}
	vs.subMu.Unlock()
}

// Subscribe returns a channel that receives JSON payloads on each update.
// Call Unsubscribe when done.
func (vs *VehicleStream) Subscribe() chan []byte {
	ch := make(chan []byte, 3)
	vs.subMu.Lock()
	vs.subs[ch] = struct{}{}
	vs.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (vs *VehicleStream) Unsubscribe(ch chan []byte) {
	vs.subMu.Lock()
	delete(vs.subs, ch)
	vs.subMu.Unlock()
	close(ch)
}

// detectStopVisits checks which stops each bus passed since its last known
// position using segment-to-point distance (same algorithm as the recorder's
// vehicleTracker). Updates lastServed timestamps in-memory.
func (vs *VehicleStream) detectStopVisits(vehicles []VehicleWithDelay, feedTS time.Time) {
	if len(vs.routeStops) == 0 {
		return
	}

	// Index stops by ID for fast lat/lon lookup
	stopLoc := make(map[string]cachedStop, len(vs.stops))
	for _, s := range vs.stops {
		stopLoc[s.id] = s
	}

	tsUnix := feedTS.Unix()

	vs.lastServedM.Lock()
	defer vs.lastServedM.Unlock()

	for i := range vehicles {
		v := &vehicles[i]
		if v.RouteID == "" || v.Lat == 0 || v.Lon == 0 {
			continue
		}

		prev, hasPrev := vs.prevPos[v.ID]

		// Update previous position for next cycle
		vs.prevPos[v.ID] = prevPosition{lat: v.Lat, lon: v.Lon, routeID: v.RouteID}

		if !hasPrev || prev.routeID != v.RouteID || prev.lat == 0 {
			continue
		}

		for _, sid := range vs.routeStops[v.RouteID] {
			s, ok := stopLoc[sid]
			if !ok {
				continue
			}

			// Point distance first (cheap)
			dist := haversineMeters(v.Lat, v.Lon, s.lat, s.lon)
			if dist > stopVisitThresholdM {
				// Segment interpolation between previous and current position
				segDist, _ := segmentDistToPoint(
					prev.lat, prev.lon, v.Lat, v.Lon,
					s.lat, s.lon,
				)
				dist = segDist
			}

			if dist <= stopVisitThresholdM {
				vs.lastServed[sid] = tsUnix
			}
		}
	}
}

// Current returns the latest JSON payload.
func (vs *VehicleStream) Current() []byte {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.current
}

// RawFeed returns the latest raw protobuf from upstream.
func (vs *VehicleStream) RawFeed() []byte {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.currentRaw
}
