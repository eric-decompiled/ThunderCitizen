# Database

## Migrations

```bash
# Run migrations (requires DATABASE_URL)
DATABASE_URL="postgres://..." make migrate-up
DATABASE_URL="postgres://..." make migrate-down
```

Migrations auto-run on server startup.

## Schema

Migrations in `migrations/`. Every transit-domain relation is prefixed with
`transit_`, mirroring the `council_*` and `budget_*` conventions.

Tables:

- **GTFS schedule** — `transit_routes`, `transit_stops`, `transit_trips`, `transit_stop_times`, `transit_calendar_dates`, `transit_transfers`
- **GTFS-RT events** — `transit_vehicle_positions`, `transit_stop_delays`, `transit_cancellations`, `transit_alerts`
- **Derived** — `transit_stop_visits` (GPS proximity detection), `transit_route_timepoints`
- **Fleet tracking** — `transit_vehicles`, `transit_vehicle_assignments`
- **Operational** — `transit_feed_state`, `transit_feed_gaps`

## PostGIS

The `db` container uses `Dockerfile.db` (Debian + `postgresql-16-postgis-3`). Geography columns on `transit_stops` and `transit_vehicle_positions` enable spatial queries:

- **Nearest stops** — KNN via `<->` operator on GiST index
- **Vehicle-to-stop distance** — `ST_Distance` between geography columns
- Triggers auto-populate `geog` on INSERT/UPDATE — no changes to write paths

### Index Strategy

The original schema had ~1.1 GB of indexes for ~500 MB of table data — several
were never used (GIS on vehicle positions, per-vehicle history) and one btree
on `last_updated` was 386 MB alone for 18 scans. The current set targets actual
query patterns and dropped total index footprint to ~170 MB.

**transit_stop_delays** (heaviest table, ~357K rows)

| Index | Type | Covers |
|-------|------|--------|
| PK `(date, trip_id, stop_id)` | btree | OTP date-range scans, trip delay lookups |
| `idx_esd_route_stop_date` | btree | Per-route per-stop metrics |
| `idx_esd_last_updated_brin` | BRIN | 24h dashboard percentile queries (24 KB vs 386 MB btree) |

**transit_stop_visits** (~180K rows)

| Index | Type | Covers |
|-------|------|--------|
| PK `(trip_id, stop_id)` | btree | Upsert on write path |
| `idx_sv_route_stop INCLUDE (observed_at)` | btree | Headway/EWT/Cv — covering index for index-only scans |
| `idx_sv_observed` | btree | Date-range headway window functions |

**transit_cancellations** (~44K rows)

| Index | Type | Covers |
|-------|------|--------|
| UNIQUE `(trip_id, feed_timestamp)` | btree | Dedup on insert, cancel detail queries |
| `idx_ec_feed_timestamp` | btree | Date-range cancel rate scans |
| `idx_ec_trip_route_start` | btree (partial) | Cancel detail GROUP BY (WHERE start_time IS NOT NULL) |

**transit_vehicle_positions** (~2.8M rows)

| Index | Type | Covers |
|-------|------|--------|
| PK `(id)` | btree | Required |
| `idx_evp_feed_timestamp` | btree | 24h dashboard, live feed queries |

**Other tables**

| Index | Table | Covers |
|-------|-------|--------|
| `idx_ea_feed_timestamp` | `transit_alerts` | Latest-alert queries |
| `idx_transit_stops_geog` | `transit_stops` | PostGIS KNN nearest-stop queries |
| `idx_transit_stop_times_stop` | `transit_stop_times` | Stop-level schedule queries |
| `idx_transit_stop_times_first_dep_time` | `transit_stop_times` (partial: stop_sequence=1) | Per-band cancel rate / OTP queries |
| `idx_transit_stop_times_tp_dep_time` | `transit_stop_times` (partial: timepoint=TRUE) | Per-band EWT scheduled-headway lookup |
| `idx_transit_trips_service_route` | `transit_trips` | Calendar→trips join (INCLUDE trip_id, route_id) |

### Schedule-headway computation

EWT and related scheduled-headway calculations are derived inline from
`transit_stop_times` joined against `transit_route_timepoints` and the
(service_id, date) pairs we observed running (via `transit_stop_delays`).
The previous `transit_sched_headways` materialized view was dropped — it
depended on `transit_calendar_dates`, which silently lapsed on long-lived
deployments whenever the GTFS bundle's coverage rolled past the queried
date range. See the `headway` recipe in `internal/transit/recipes/`
and the chunk orchestrator in `internal/transit/chunk.go`.

### Metric rollup table — `transit.route_band_chunk`

The chunk-based metrics read path stores one row per (route, date, band)
in `transit.route_band_chunk` (added in migration `000003`, formerly
`transit.route_band_bucket`). Columns are raw counts plus SUM-stable
headway sums (`headway_sum_sec`, `headway_sum_sec_sq`, `sched_headway_sec`),
never percentages — aggregation happens in Go via `KPIFromChunks` in
`internal/transit/view_helpers.go` and the matching JS port in
`static/transit/chunks.js`. The orchestrator that fills this table is
`BuildChunksForDate` in `internal/transit/chunk.go`, which calls five
per-metric recipes from `internal/transit/recipes/` against the upstream
event tables.

### Postgres Tuning

| Setting | Default | Current | Why |
|---------|---------|---------|-----|
| `work_mem` | 4 MB | 16 MB | Eliminates disk-spill sorts in headway window functions |
| `shared_buffers` | 128 MB | 256 MB | Keeps hot tables (stop_visits, transit_stop_delays) in memory |

## Connection

Uses `pgx/v5` with connection pooling. Pool configured in `internal/database/db.go`:

- Max connections: 25
- Min connections: 5
- Max lifetime: 1 hour
- Max idle time: 30 minutes
- **`DefaultQueryExecMode = QueryExecModeCacheDescribe`** — caches parameter
  type descriptions (fast protocol) but re-plans every query. The default
  `QueryExecModeCacheStatement` caches the full prepared plan, and after 5
  executions Postgres switches from a "custom plan" (replanned with actual
  parameter values) to a "generic plan" (planned once with no parameter
  info). For the per-band metric queries with selective `departure_time`
  range filters, the generic plan picks a pathological join order and the
  same query that runs in 150 ms takes 30+ seconds. Re-planning every call
  is cheap relative to the actual work the query does; see the
  `internal/database/db.go` comment for the incident history.

## Data Loading at Startup

`cmd/server/main.go` loads data after migrations:

1. `transit.LoadStaticGTFS(ctx, db)` — Routes, stops, trips, stop_times, calendar_dates from GTFS CSV files. After loading the CSVs, this also:
   - Refreshes `transit_route_timepoints` from the new stop_times
   - Derives display names from headsigns where `long_name` is empty
   - Runs `ANALYZE` on `transit_stop_times`, `transit_trips`, `transit_stops`,
     and `transit_route_timepoints` so the planner has fresh statistics.
     Bulk loads don't trigger autoanalyze, and stale stats caused the
     per-band metric queries to pick pathological seq-scan plans.
2. `data.LoadFIRFromDB(ctx, db)` — FIR budget data, merged into `BudgetByYear`
