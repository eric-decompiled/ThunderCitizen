# Transit Data

## Transit Planning Vocabulary

| Term | Definition | GTFS field |
|------|-----------|------------|
| **Time point** | A stop where schedule adherence is measured, located at major intersections or key destinations (hospitals, colleges, terminals). | `transit_stop_times.timepoint = TRUE` |
| **Terminal** | The endpoint of a route. Circular routes have one terminal; linear routes have two. Terminals provide driver amenities (restrooms, refreshments). Thunder Bay has two: City Hall and Waterfront. | `transit_stops.is_terminal` |
| **Block** | The full sequence of trips one bus performs in a service day. A single block may span multiple routes. When a bus fails, all remaining trips in the block are affected. | `transit_trips.block_id` |
| **Block interruption** | A contiguous span of cancelled trips within a block — not a "block cancellation" (the block itself isn't cancelled, it's interrupted and may recover). | Derived from `transit_cancellations` + `transit_trips.block_id` |
| **Headsign** | The destination displayed on the front of the bus, used to distinguish trip direction. | `transit_trips.headsign` |
| **Deadhead** | Non-revenue movement of a bus between trips (e.g., repositioning from one route to another within a block). Not tracked in GTFS. | — |
| **Layover** | Recovery time between consecutive trips in a block. Thunder Bay averages 3.7 min on route changes and 1.5 min within the same route. | Derived from `transit_stop_times` |

## Upstream API

Thunder Bay Transit exposes GTFS-RT (General Transit Feed Specification — Realtime) feeds via NextLift:

| Feed | URL | Format | Update Frequency |
|------|-----|--------|-----------------|
| Vehicle Positions | `http://api.nextlift.ca/gtfs-realtime/vehicleupdates.pb` | Protobuf | ~15s |
| Trip Updates | `http://api.nextlift.ca/gtfs-realtime/tripupdates.pb` | Protobuf | ~30s |
| Service Alerts | `http://api.nextlift.ca/gtfs-realtime/alerts.pb` | Protobuf | ~30s |
| Static GTFS | `http://api.nextlift.ca/gtfs.zip` | ZIP/CSV | Periodic |

No authentication required. No documented rate limits, but feeds are polled respectfully with ±10% jitter on each interval.

### Feed Contents

**Vehicle Positions** — GPS location of each active bus: lat/lon, bearing, speed, current stop status, occupancy, assigned route/trip.

**Trip Updates** — Per-stop delay predictions for active trips. Each `StopTimeUpdate` has `arrival.time` (absolute Unix timestamp) and `arrival.delay` (seconds). Also carries trip-level `ScheduleRelationship`:
- `SCHEDULED` (normal), `CANCELED`, `DELETED` (trip won't run)
- `ADDED`, `REPLACEMENT`, `DUPLICATED`, `NEW` (extra service)

**Service Alerts** — Active disruptions: detours, cancellations, stop closures. Each alert has cause/effect enums, severity, active time periods, and lists of affected routes/stops.

### Static GTFS

Route, stop, trip, and schedule data from CSV files, loaded into Postgres at server startup. Fetched via `./bin/fetcher gtfs`.

**Important:** The city periodically updates trip and stop IDs in the static GTFS without notice. The live GTFS-RT feed uses the new IDs immediately, so trip-level joins between static GTFS and live feed can break. Stop predictions (`stops.go`) use absolute timestamps from the live feed to avoid this issue.

## `internal/transit` Package

### Recorder (`recorder.go`)

Polls all three feeds in background goroutines:

```
pollLoop(vehicles, 15s) → recordVehicles → transit_vehicle_positions + vehicleTracker
pollLoop(trips, 30s)    → recordTrips    → transit_stop_delays + transit_cancellations
pollLoop(alerts, 30s)   → recordAlerts   → transit_alerts
```

The recorder is a pure event writer — no in-memory aggregation. Dashboard stats are derived on-the-fly from event tables via windowed SQL queries.

**`recordTrips`** upserts `transit_stop_delays` (one row per trip-stop per day) and inserts `transit_cancellations`.

**`recordVehicles`** inserts into `transit_vehicle_positions` and calls the vehicle tracker. The tracker upserts the `transit_vehicles` fleet table, records `transit_vehicle_assignments` (vehicle-to-trip mapping), and writes `transit_stop_visits` rows for any stop the bus just reached (see Stop Visit Detection below). The live map fetches directly via the CORS proxy (`/api/transit/vehicles`).

### Client (`client.go`)

GTFS-RT protobuf client. Parses feeds into Go types. Used by:
- Recorder (background polling)
- Stop predictions (on-demand for popup arrivals)
- Vehicle proxy (raw pass-through for live map)

### Metrics — chunk model (`internal/transit/chunk.go`, `internal/transit/chunk/`, `internal/transit/recipes/`)

Computes transit performance from `transit_stop_delays`, `transit_stop_visits`,
`transit_stop_times`, and `transit_cancellations`. The metric unit is a
**chunk**: 1 route × 1 day × 1 band (Morning 6–12 / Midday 12–18 / Evening 18–24),
persisted as one row in `transit.route_band_chunk` (migration `000003`).

- **Raw counts only** — Each chunk stores `trip_count`, `on_time_count`,
  `scheduled_count`, `cancelled_count`, `no_notice_count`, `headway_count`,
  `headway_sum_sec`, `headway_sum_sec_sq`, `sched_headway_sec`. Never
  pre-computed percentages. Aggregating already-rounded percentages is
  wrong; aggregating raw counts and dividing once at the end is exact
  arithmetic.
- **Recipes** — `BuildChunksForDate` runs five small per-metric SQL
  queries from `internal/transit/recipes/` for each (route, date, band)
  tuple: `service_kind`, `otp`, `cancel`, `baseline`, `headway`. Each
  recipe is its own file with one SQL constant and one Go function so
  the formulas can be audited in isolation. The orchestrator stitches
  recipe outputs into a `chunk.Chunk` and upserts it.
- **No calendar_dates dependency** — The "scheduled trips" baseline is
  reconstructed from `transit_stop_times` joined to the `(service_id, date)`
  pairs the recorder observed running (via `transit_stop_delays`), not from
  `transit_calendar_dates`. Long-lived prod DBs whose GTFS bundle has
  rolled past the queried range still produce correct numbers.
- **Read path** — `Service.Chunks(ctx, from, to)` calls `ChunkCache.Range`
  in `internal/transit/chunk_cache.go`. The cache lazy-loads from the
  rollup table and stores per (route, date, band) forever; only "today"
  is allowed to refresh.
- **Aggregation** — `KPIFromChunks` and `RouteRowKPIFromChunks` in
  `view_helpers.go` SUM raw counts across the requested slice, then
  divide once at the end. Empty band string pools all three.
- **Math** — Pure formulas in `internal/transit/chunk/math.go` with
  textbook unit tests. SUM-stable identities:
  `Cv = √(E(X²) − E(X)²) / E(X)`, `EWT = Σ(h²)/(2·Σh) − sched_h/2`,
  `Wait = Σ(h)/N`. Same formulas mirrored in
  `static/transit/chunks.js` (`window.transitChunks.aggregate`) so the
  client-side route comparison chart and the server-rendered KPI cards
  always agree.
- **Rebuilding** — `./bin/fetcher chunks` interactively rebuilds chunks
  for a date range against the live event tables. `./bin/seedtransit`
  writes synthetic chunks for the dev DB (one per route × day × band)
  with a linear bad→good trend and per-route quality bias.

### Cache layer (`cache.go`, `repo_cache.go`, `chunk_cache.go`)

Two cache structs:

1. **`ChunkCache`** (`chunk_cache.go`) — the metrics read layer. Keyed by
   (route_id, date, band), backed by `transit.route_band_chunk`. Lazy-loads
   on first access for a date and stores forever; "today" is the only key
   allowed to be re-read after midnight rolls over. Three methods:
   `One(routeID, date, band)`, `Range(from, to)`, `EarliestDate()`.
2. **`RepoCache`** (`repo_cache.go`) — everything else. A single struct
   holding `CacheSlot[T]` and `CacheMap[K,V]` instances for non-metric
   data products. The primitives live in `cache.go` and implement a
   double-checked-locking lazy-load pattern: readers grab an `RLock`,
   on miss upgrade to `Lock`, re-check, call the loader, store the result.

**Strategy: lazy-load on cold read, cache forever.** No background
warmers, no periodic refresh. The first caller after boot pays the
compute cost; every subsequent caller hits memory. Being slow on the
first request is a fine trade for not having to reason about a warming
schedule.

`RepoCache` slots:

| Cache | Type | Key | TTL |
|---|---|---|---|
| `routeMeta` | `CacheSlot[[]RouteMetaAPI]` | — | ∞ |
| `allStops` | `CacheSlot[[]Stop]` | — | ∞ |
| `stopAnalytics` | `CacheSlot[[]StopAnalyticsRow]` | — | ∞ |
| `stats` | `CacheMap[string, *StatsReport]` | `"day" \| "percentiles" \| "week"` | ∞ |
| `live` | `CacheSlot[*liveData]` | — | **30 s** |

Only `live` has a TTL. Everything else caches until either the process
restarts or the cache key changes.

KPIs have no separate endpoint or cache — they're rendered straight into
the metrics page via `KPIFromChunks(vm.Chunks, metric, band)` in
`view_helpers.go`, and the same chunks are embedded via
`@templ.JSONScript("transit-chunks", vm.Chunks)` so the frontend
(`chunks.js`, `trends-chart.js`) can recompute any aggregation it needs
without a fetch.

### Browser Cache-Control

- **HTML pages** (`/`, `/budget`, `/councillors`, `/minutes`, `/motions`,
  `/about`): `public, max-age=300` — generous enough that refresh and
  back-nav hit the browser cache, short enough that content updates
  propagate within 5 minutes. Set by a `PageCache` middleware applied to
  the page route group in `cmd/server/main.go`.
- **Static GTFS-derived JSON** (`/api/transit/routes`, `/api/transit/stops`,
  `/api/transit/timepoints`): `cache.Reference` (`public, max-age=3600, immutable`) —
  tells the browser not to revalidate within the hour. Safe because the
  underlying data only changes when GTFS reloads, which is infrequent
  and a short stale window is acceptable.
- **Livish endpoints** (`/api/transit/stats`, `/api/transit/stop/*/predictions`,
  `/api/transit/vehicle/*/distance/*`, `/api/transit/stops/nearby`):
  `cache.Short` (30s). Cacheable but not immutable.
- **Live endpoints** (`/api/transit/vehicles*`): `cache.Live` (`no-cache`).
  Event-driven and must always reflect the latest feed.
- **Static assets** (`/static/*` — councillor photos, CSS/JS, PMTiles
  basemap, budget JSON): `public, max-age=604800, immutable` — a week.
  To update a file, either bump the filename (cache-busting URL) or
  accept up to a week of stale.

### Vehicle Tracker (`vehicle_tracker.go`)

Tracks the vehicle fleet and detects stop visits:

- **Fleet tracking** — Upserts `transit_vehicles` table with first/last seen timestamps
- **Trip assignments** — Records which vehicle served which trip on each date
- **Stop visit detection** — For each 15-second position update, checks whether the bus is within 50m of any stop on its route. Uses both point distance (current GPS fix) and line-segment distance (between the previous and current fix) via `segmentDistToPoint`, so stops the bus passed between readings still get recorded. First sighting per `(trip, stop)` wins via an in-memory dedup map
- **Crossing time interpolation** — When a visit is matched via segment distance, the observed_at timestamp is interpolated along the segment rather than snapped to the latest feed timestamp
- **Caches** — Stop locations and route→stop membership loaded once via `sync.Once` after GTFS is available

### Stops (`stops.go`)

- **`AllStops`** — Returns all stops with coordinates, route count, and transfer flag
- **`StopPredictions`** — Fetches live GTFS-RT trip updates, filters for a specific stop, and returns predicted arrivals with delay status. Uses absolute timestamps from the feed (no dependency on matching GTFS static trip IDs). Route display info (name, color) from the `transit_routes` table.

### Queries (`queries.go`)

| Function | Returns | Source |
|----------|---------|--------|
| `DayPercentiles` | P50/P90/P99/P99.9 in 30-min buckets (24h) | `transit_stop_delays` bucketed by `last_updated` |
| `DaySnapshots` | 5-min system stats (24h) | Derived on-the-fly from all `transit_*` event tables |
| `WeekSummary` | Daily aggregates (7d) | Derived from `transit_stop_delays` + `transit_cancellations` |
| `RouteSchedule` | Today's trips for a route | `transit_trips` + `transit_stop_times` + `transit_stop_delays` |
| `CurrentAlerts` | Alerts from latest feed poll | `transit_alerts` |
| `CancelledRoutes` | Routes with cancellations in latest feed poll | `transit_cancellations` |

### Trip Planner (`raptor.go`)

RAPTOR-based trip planner that finds optimal bus routes between any two points. Implements the algorithm from "Round-Based Public Transit Routing" (Delling, Pajor, Werneck — Microsoft Research, 2012).

**What this is and isn't:** Our planner answers "which buses connect A to B" — it finds routes through Thunder Bay's transit network. It assumes a 400m (~5 min) walking radius and uses crow-flies distance. Google Maps solves a different problem: "how do I physically get there" — pedestrian routing on real sidewalks, real-time delay integration, fare optimization. Same core algorithm (RAPTOR), different tuning. Ours is a network discovery tool, not turn-by-turn navigation.

**Algorithm (3-step rounds per the paper):**
- Loads the full GTFS timetable into memory (~162k stop_times), cached per service day via `TimetableCache`
- Precomputes foot-path transfers between all stop pairs within 400m at timetable build time
- Runs in rounds: round 1 = direct trips, round 2 = one transfer, up to round 4 (3 transfers)
- Each round: (1) collect routes at marked stops, (2) scan each route for earliest catchable trip, (3) relax foot-path transfers to nearby stops
- Uses separate per-round arrival arrays (`tauPrev`/`tauCurr`) — boarding decisions use previous round's times, preventing same-round double-boarding
- Returns Pareto-optimal itineraries sorted by fewest transfers, then fastest

**Origin/destination handling:**
- If user selects a specific stop, pins to that exact stop (0m walk)
- If using lat/lon (e.g. "My Location"), finds all stops within 400m as boarding candidates
- Destination uses all stops within 400m so the algorithm can alight early and walk

**Features:**
- **Cancellation awareness** — fetches current cancelled trips fresh (not cached), runs RAPTOR twice (with/without cancellations), shows cancelled itineraries with time delta if significantly faster
- **Next departure** — runs a second RAPTOR query after the first bus to show the next option
- **Arrive-by mode** — binary search over forward RAPTOR runs to find latest departure arriving on time
- **"Stay on" hints** — when the last transit leg's route continues closer to the destination, suggests staying on instead of transferring
- **Leave-by time** — computes when to leave based on first bus departure minus walk time
- **Map visualization** — draws route shapes between board/alight stops, time labels at start/transfers/arrival, dims irrelevant buses

### Stop Visit Detection — Calibration

The 50m threshold used by the Vehicle Tracker was calibrated against 22K
GTFS-RT `STOPPED_AT` observations: P50 = 11m, P95 = 48m. At 50 km/h with
15-second polling that's ~200m of unobserved travel between fixes, which is
why the segment-distance check (in addition to point distance) is necessary —
without it the tracker would miss stops the bus drove past between two GPS
readings. `transit_stop_visits` is the primary source for headway, bunching,
Cv, and EWT calculations.

### Handler (`handler.go`)

HTTP adapter layer. Routes split into page routes (mounted at `/transit`)
and API routes (mounted at `/api/transit`). Handlers call `Service`
accessor methods exclusively — they never touch the Reporter or Repo
directly. This is the boundary where "warming" state is decided: a handler
that gets `nil` from an accessor returns 503 (cache still warming) rather
than synthesising an empty response.

### Service (`service.go`)

Thin delegator between Handler and RepoCache. Holds the reporter, vehicle
stream, and cache. Every cached accessor is a one-liner that calls
`s.cache.X.Peek()` (for always-warmed slots) or `s.cache.X.Get(ctx, key)`
(for lazy-loadable keyed slots). Also houses the couple of uncached
convenience queries (`RouteServiceDays`, `RouteCancelDays`,
`RouteTrackingStats`) that hit the DB directly.

### Reporter (`reporting.go`)

Assembles complete reports from repo queries and client data. Each method
returns a typed report struct. Called by the `RepoCache` loaders, not by
the Handler.

### Repository (`repo.go`)

Database access layer. Plain SQL via `pgx/v5` — no ORM. Methods for stops,
routes, alerts, cancellations, snapshots, percentiles, feed state, and
timetable loading.

### Static GTFS Loader (`gtfs_loader.go`)

Loads CSV files from `static/transit/gtfs/` into `transit_routes`, `transit_stops`, `transit_trips`, `transit_stop_times`, `transit_calendar_dates`, `transit_transfers` tables at startup. Truncates and reloads each time.

### Protobuf (`gtfsrt/`)

Generated Go types from the [GTFS-RT proto spec](https://gtfs.org/realtime/proto/). `gtfs-realtime.pb.go` — do not edit.

## Database Schema

### Event Tables

```
transit_vehicle_positions — vehicle GPS positions from every poll
transit_stop_delays       — (date, trip_id, stop_id) PK, upserted from GTFS-RT trip updates
transit_cancellations     — (trip_id, feed_timestamp) unique, append-only
transit_alerts            — (alert_id, feed_timestamp) unique, append-only
```

### Fleet & Operational Tables

```
transit_vehicles            — vehicle_id PK, first/last seen timestamps
transit_vehicle_assignments — (date, vehicle_id, trip_id) PK, vehicle-to-trip mapping
transit_feed_state          — last processed timestamp per feed type
transit_feed_gaps           — detected gaps in polling
```

### Schedule Tables (from static GTFS)

```
transit_routes          — route_id PK, short_name, long_name, color
transit_stops           — stop_id PK, stop_name, lat, lon, wheelchair, geog (PostGIS)
transit_trips           — trip_id PK, route_id, service_id, headsign
transit_stop_times      — (trip_id, stop_sequence) PK, arrival/departure times
transit_calendar_dates  — (service_id, date) unique, exception_type
transit_transfers       — (from_stop_id, to_stop_id) unique, official transfer points
```

### Stop Visits

```
transit_stop_visits — (date, trip_id, stop_id) PK
                      route_id, vehicle_id, observed_at, distance_m
                      idx_sv_route_stop_date, idx_sv_stop_date, idx_sv_observed
```

### Spatial (PostGIS)

```
transit_stops.geog              — geography(Point, 4326), auto-populated via trigger
transit_vehicle_positions.geog  — geography(Point, 4326), auto-populated via trigger
idx_transit_stops_geog          — GiST index for KNN nearest-neighbor queries
idx_evp_geog                    — GiST index for vehicle proximity queries
```

## HTTP Endpoints

### Page routes (mounted at `/transit`)

| Endpoint | Purpose |
|----------|---------|
| `GET /transit` | Live map, trip planner, live stats bar |
| `GET /transit/metrics` | KPI cards + trend chart + route comparison |
| `GET /transit/routes` | Route directory (table of all routes) |
| `GET /transit/method` | Methodology documentation |
| `GET /transit/route/{id}` | Route detail: metrics, alerts, stop performance, schedule |
| `GET /transit/report` | Permanent redirect to `/transit` |

### API routes (mounted at `/api/transit`)

| Endpoint | Purpose |
|----------|---------|
| `GET /api/transit/vehicles` | Proxies upstream vehicle protobuf (CORS) |
| `GET /api/transit/vehicles.json` | Vehicle positions as JSON (same data as the protobuf proxy) |
| `GET /api/transit/vehicles/stream` | Server-Sent Events stream of vehicle position updates |
| `GET /api/transit/stats` | 24h system stats (snapshots, percentiles, or week summary) |
| `GET /api/transit/routes` | Route metadata (names, colors, terminals) derived from GTFS at boot |
| `GET /api/transit/stops` | All stops with coordinates, route count, transfer flag |
| `GET /api/transit/stops/nearby` | Nearest stops to a lat/lon (PostGIS KNN) |
| `GET /api/transit/stops/analytics` | Per-stop aggregate metrics |
| `GET /api/transit/stop/{id}/predictions` | Live arrival predictions from GTFS-RT |
| `GET /api/transit/timepoints` | GTFS timepoint stops used by schedule-adherence metrics |
| `GET /api/transit/plan` | Trip planner (RAPTOR algorithm) |
| `GET /api/transit/vehicle/{id}/distance/{stopID}` | Distance from vehicle to stop (PostGIS) |

### Query Parameters

**`/api/transit/stats`**
- `range=percentiles` — P50/P90/P99/P99.9 delay curves in 30-min buckets (24h)
- `range=week` — daily on-time summaries for last 7 days
- *(default)* — 5-min system snapshots derived from events (24h)

> Metric KPIs are NOT exposed as a JSON API. They're computed via
> `KPIFromChunks` and rendered straight into `/transit/metrics` and
> `/transit/routes`; the underlying chunks ride along in the page via
> `@templ.JSONScript("transit-chunks", vm.Chunks)`.

**`/api/transit/stops/nearby`**
- `lat`, `lon` (required) — query point
- `limit=N` (default 10, max 50) — number of results

**`/api/transit/plan`**
- `from_lat`, `from_lon`, `to_lat`, `to_lon` (required) — origin/destination coordinates
- `from_stop`, `to_stop` (optional) — pin to exact stop ID (0m walk)
- `depart=HH:MM` (default: now) — departure time
- `arrive_by=HH:MM` — arrive-by mode (binary search for latest departure)
- `date=YYYY-MM-DD` (default: today) — service date

## Development

Two ways to get data into the dev DB:

1. **Real data via the recorder.** Bring up the dev server and let the
   in-process GTFS-RT recorder collect for a few minutes — it populates
   `transit_vehicle_positions`, `transit_stop_delays`, `transit_stop_visits`,
   etc. Then `./bin/fetcher chunks` rolls those events into
   `transit.route_band_chunk` for whatever date range you ask for.
2. **Synthetic chunks via `seedtransit`.** `go run ./cmd/seedtransit` (or
   `./bin/seedtransit`) writes synthetic chunks directly to
   `transit.route_band_chunk` — one per route × day × band — with a linear
   bad→good trend and per-route quality bias. Skips the event tables
   entirely. Useful for fresh DBs without GTFS loaded, or when you want
   reproducible numbers without waiting for the recorder. See
   [cmd/seedtransit/README.md](../cmd/seedtransit/README.md). **Dev only —
   not bundled into the production Docker image.**
