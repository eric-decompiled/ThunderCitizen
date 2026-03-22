# Transit Performance Metrics — Research Compendium

Research into industry-standard transit KPIs, measurement methodologies, and academic literature to inform what ThunderCitizen should compute from its GTFS-RT data (`transit_stop_delays`, `transit_vehicle_positions`, `transit_stop_visits`, `transit_cancellations`).

---

## 1. Core Metrics Taxonomy

### Schedule Adherence (low-frequency routes, headway > 10 min)

The standard for **on-time performance (OTP)**. A trip is "on-time" if it departs within a window around the scheduled time. **There is no unified national standard** — agencies define their own windows:

| Agency | Early Limit | Late Limit | Window |
|--------|------------|------------|--------|
| Most US agencies | 1 min early | 5 min late | 6 min |
| SFMTA | 1 min early | 4 min late | 5 min |
| WMATA (DC) | 2 min early | 7 min late | 9 min |
| MnDOT | 1 min early | 4 min late | 5 min |
| Japan (all rail) | 0 | 1 min late | 1 min |
| Tokyo Metro (departure) | 15 sec early | 15 sec late | 30 sec |

**Thunder Bay currently uses ±60 seconds** — extremely tight by North American standards. A more standard threshold would be **-1 min / +5 min** (the most common US definition).

**Source:** [TransitCenter: Your Bus Is On Time. What Does That Even Mean?](https://transitcenter.org/bus-time-even-mean/)

### Headway Adherence (high-frequency routes, headway ≤ 10 min)

For frequent service, passengers don't consult schedules — they just show up. **On-time performance is the wrong metric.** What matters is the **regularity of spacing between buses**. Jarrett Walker (Human Transit) argues this is the fundamental metric for any frequent service.

Key measures:
- **Coefficient of Variation of Headway (Cv.h)** = σ(headway) / mean(headway). Lower = more regular. Perfect service = 0.
- **Wait Assessment** = % of observed headways within ±2 min of scheduled headway (TfL uses ±2 min).
- **Headway Deviation** = actual headway − scheduled headway per stop per trip pair.

**Source:** [Human Transit: Beyond On-Time Performance](https://humantransit.org/2010/10/beyond-on-time-performance.html)

### Excess Waiting Time (EWT) — The Gold Standard

Developed by **Transport for London** and validated by Imperial College London's International Bus Benchmarking Group (IBBG). EWT is the **difference between actual passenger wait time and scheduled wait time**.

```
EWT = Actual Wait Time − Scheduled Wait Time
```

For a route with scheduled headway H:
- **Scheduled Wait Time (SWT)** = H/2 (assumes random passenger arrivals)
- **Actual Wait Time (AWT)** = Σ(h²) / (2 × Σh) where h = observed headways

EWT is considered the **most statistically robust** regularity metric because:
- It's customer-centric (measures what passengers experience)
- It normalizes across different headways
- A late bus can be "counted as the next bus running early" — matching how riders perceive it

TfL publishes EWT quarterly for every route via their **Quality of Service Indicators (QSI)** system, computed from iBus AVL data at representative timing points.

**Source:** [TfL Bus Performance Data](https://tfl.gov.uk/corporate/publications-and-reports/buses-performance-data), [Imperial College IBBG research](https://www.researchgate.net/publication/254609206_Development_of_Key_Performance_Indicator_to_Compare_Regularity_of_Service_Between_Urban_Bus_Operators)

### Mean Waiting Time Formula

From TCRP Report 13907 (Using Archived AVL-APC Data):

```
E[W] = (H/2) × (1 + Cv.h²)
```

Where H = mean headway, Cv.h = coefficient of variation. This means irregular service always increases wait times — even if average headway is correct, variance penalizes riders.

**Source:** [NAP: Tools for Analyzing Waiting Time](https://nap.nationalacademies.org/read/13907/chapter/8)

---

## 2. World-Class Operators

### Transport for London (TfL)
- **Primary metric:** Excess Waiting Time (EWT) — the global benchmark for bus reliability
- **Measurement:** iBus AVL system at QSI timing points, all-day every-day, 05:00–23:59
- **Supplementary:** Scheduled km operated, bus speeds (mph including dwell), passenger satisfaction scores
- **Approach:** Passenger-perspective — a late bus is treated as the next bus running early
- **Source:** [TfL QSI Performance Results](https://bus.data.tfl.gov.uk/boroughreports/current-quarter.pdf)

### MTR Corporation (Hong Kong)
- **99.9% punctuality** — 999 of every 1,000 passengers arrive within 5 minutes of schedule
- Reports three distinct metrics: **Train Service Delivery**, **Passenger Journeys On-Time**, **Train Punctuality**
- Must report all delays > 8 minutes to government
- 5.7M daily passengers, farebox recovery ratio of 187% (world's highest)
- **Source:** [Railway News: MTR Maintains 99.9% Punctuality](https://railway-news.com/mtr-corporation-maintains-punctuality-rate/)

### Tokyo Metro / JR
- **Threshold:** 1 minute late = delayed (Japan-wide standard)
- **Shinkansen average delay:** ~20 seconds; commuter trains: ~50 seconds
- **Departures measured to 15-second precision** — neither early nor late by >15s
- Dwell time at stations is the critical measurement point (~90% of total delays < 5 min)
- **Source:** [JRailPass: Japan Train Punctuality](https://www.jrailpass.com/blog/japan-train-punctuality), [Metro Magazine: Tokyo Metro Rush Hour](https://www.metro-magazine.com/10007392/how-the-tokyo-metro-handles-rush-hour-to-operate-on-time)

---

## 3. Bus Bunching & Big Gaps

Bus bunching = two or more buses arriving together, leaving a big gap behind. This is **the most visible reliability failure** for riders.

**Detection from GTFS-RT:**
- **Bunching:** Headway < 25% of scheduled headway (two buses nose-to-tail)
- **Big Gap:** Headway > 175% of scheduled headway
- Three headway states: **Bunching**, **Stable**, **Big Gap**

Research uses headway coefficient of variation and prediction models (LS-SVM, neural nets) to forecast bunching before it occurs.

**Source:** [Headway-based bus bunching prediction](https://trid.trb.org/view.aspx?id=1427445), [TTC Riders bunching report](https://www.ttcriders.ca/bunchingreport)

---

## 4. TCQSM Service Frequency Levels

The Transit Capacity and Quality of Service Manual (TCRP Report 165, 3rd ed.) defines service levels by headway:

| Headway | Behavior | TCQSM Category |
|---------|----------|-----------------|
| ≤ 5 min | No need to check schedule | Very frequent |
| 5–10 min | Passengers may check schedule | Frequent |
| 11–15 min | Passengers check schedule | Moderate |
| 16–30 min | Must plan around schedule | Infrequent |
| 31–60 min | Significant planning required | Low |
| > 60 min | Limited mobility | Minimal |

Thunder Bay routes mostly operate at 15–30 min headways → **schedule adherence is the right metric** (not headway adherence).

**Source:** [TCRP Report 165](https://nap.nationalacademies.org/catalog/24766/transit-capacity-and-quality-of-service-manual-third-edition)

---

## 5. Key Reference Documents

### Authoritative Standards
- **TCRP Report 88** — A Guidebook for Developing a Transit Performance-Measurement System. 400+ measures cataloged, recommended core set. [PDF](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_report_88/guidebook.pdf)
- **TCRP Report 141** — Methodology for Performance Measurement and Peer Comparison. Benchmarking framework for bus and rail. [PDF](https://ftis.org/iNTD-Urban/tcrp_141.pdf)
- **TCRP Report 165 (TCQSM 3rd ed.)** — Transit Capacity and Quality of Service Manual. Service levels, capacity methods, LOS framework. [NAP](https://nap.nationalacademies.org/catalog/24766/transit-capacity-and-quality-of-service-manual-third-edition)
- **TCRP Report 13907** — Using Archived AVL-APC Data to Improve Transit Performance. Wait time formulas, headway analysis tools. [NAP](https://nap.nationalacademies.org/read/13907/chapter/8)

### Academic Papers
- **Measurement and classification of transit delays using GTFS-RT data** (Springer, 2022) — Framework for systematic vs. stochastic delay classification from GTFS-RT. [Link](https://link.springer.com/article/10.1007/s12469-022-00291-7)
- **Definition and Properties of Alternative Bus Service Reliability Measures at the Stop Level** (McGill) — Compares PIR, DIS, EIS metrics. [PDF](https://tram.mcgill.ca/Research/Publications/BusReliability.pdf)
- **Passenger Travel Time Reliability for Multimodal Journeys** (TRR, 2019) — Buffer time metrics from smartcard + AVL data. [SAGE](https://journals.sagepub.com/doi/10.1177/0361198118825459)
- **Examining associations with on-time performance** (ScienceDirect, 2023) — Road network, demographic, and land use factors affecting bus OTP. [Link](https://www.sciencedirect.com/science/article/pii/S2772586323000266)
- **Gini Index for Evaluating Bus Reliability** (HAL, 2016) — Uses inequality measure for headway regularity. [PDF](https://hal.science/hal-01301646/document)
- **Waiting time and headway modeling considering unreliability** (ScienceDirect, 2021) — Theoretical + empirical headway distribution models. [Link](https://www.sciencedirect.com/science/article/pii/S0965856421003001)

### Open Source Implementations
- **MBTA transit-performance** — Real-time performance measurement from GTFS + GTFS-RT. Measures travel time, headway, dwell time, OTP, schedule adherence, passenger-weighted wait times. [GitHub](https://github.com/mbta/transit-performance)
- **MobilityData awesome-transit** — Curated list of transit tools, datasets, and APIs. [GitHub](https://github.com/MobilityData/awesome-transit)
- **California GTFS Digest** — Statewide GTFS quality and performance reporting. [Link](https://analysis.dds.dot.ca.gov/gtfs_digest/README.html)

### Canadian Context
- **CUTA** publishes annual operating/financial statistics and performance indicators for all Ontario transit systems via the Ontario Ministry of Transportation. [Ontario Open Data](https://data.ontario.ca/dataset/regional-and-municipal-transit-data)
- **MTI Report 12-58** — Transit Performance Measures in California. Comprehensive review of measures across agencies. [PDF](https://transweb.sjsu.edu/sites/default/files/1208-transit-performance-measures-in-california.pdf)

---

## 6. What ThunderCitizen Computes

Data sources: `transit_stop_delays`, `transit_vehicle_positions`, `transit_stop_visits`, `transit_cancellations`, GTFS static schedule.

### System-Level Metrics (6 KPI cards)

All cards show three time-of-day bands — **Morning (6–12) / Midday (12–18) / Evening (18–24)** — and a system-wide main value. The main value is the trip-weighted SUM of raw counts across all chunks in the active range, divided once at the end.

| Card | Data Source | Partitioning |
|------|------------|--------------|
| **OTP** | `transit_stop_delays` vs `transit_stop_times` | Trips grouped by first-stop departure hour |
| **Cancellation Rate** | `transit_cancellations` vs observed-trip baseline | First-stop departure hour |
| **Cancel Notice** | `transit_cancellations` `feed_timestamp` vs `start_time` | First-stop departure hour |
| **Stop Wait** | `transit_stop_visits` headways | Per-route headway gaps at each timepoint stop |
| **EWT** | `transit_stop_visits` vs inline-computed schedule | Per-route; schedule from `transit_stop_times` filtered by observed service days |
| **Headway Cv** | `transit_stop_visits` headways | Per-route at each stop |

**Why Cv is per-route:** Cv measures spacing regularity of a single service. Pooling multiple routes at a stop creates artificial variance from interleaving — a perfectly regular 20-min Route 1 and 30-min Route 5 produce highly variable 2/18/12/8-minute gaps. Cv at the chunk level captures the rider's experience of one route they're waiting for.

### Implementation — chunk model

The metric unit is a **chunk**: 1 route × 1 day × 1 band, persisted as one row in `transit.route_band_chunk` (migration `000003`). Each chunk stores raw counts and SUM-stable headway sums, never percentages:

```
trip_count, on_time_count                           -- OTP
scheduled_count, cancelled_count, no_notice_count   -- cancel rate, notice
headway_count, headway_sum_sec, headway_sum_sec_sq  -- wait, EWT, Cv
sched_headway_sec                                   -- EWT reference
```

Storing sums (not means) is the load-bearing decision. Aggregating already-rounded percentages is wrong; aggregating raw counts then dividing once at the end is exact arithmetic — the same number whether you compute it from one chunk or 420.

**Write path (`internal/transit/chunk.go::BuildChunksForDate`).** For each (route, date, band) tuple, the orchestrator runs five small per-metric "recipes" from `internal/transit/recipes/`, each its own file with one SQL constant and one Go function:

| Recipe | What it computes |
|--------|------------------|
| `service_kind.go` | weekday / saturday / sunday classification |
| `otp.go` | `trip_count`, `on_time_count` from `transit_stop_delays` |
| `cancel.go` | `scheduled_count`, `cancelled_count`, `no_notice_count` from `transit_cancellations` |
| `baseline.go` | scheduled-trip baseline from observed `(service_id, date)` pairs in `transit_stop_times` |
| `headway.go` | `headway_count`, `headway_sum_sec`, `headway_sum_sec_sq`, `sched_headway_sec` from `transit_stop_visits` |

The orchestrator stitches the recipe outputs into a `chunk.Chunk` and upserts it. Each recipe is auditable in isolation — the formula, the SQL, and the test sit in one file with no cross-coupling.

**Read path.** `Service.Chunks(ctx, from, to)` calls `ChunkCache.Range` (`internal/transit/chunk_cache.go`), which lazy-loads from `transit.route_band_chunk` and caches forever per (route, date, band). Today is the only key allowed to refresh; everything else is immutable history.

**Aggregation.** `KPIFromChunks` and `RouteRowKPIFromChunks` in `internal/transit/view_helpers.go` SUM the raw counts across whatever slice you hand them, then divide once at the end. Empty band (`""`) pools all three. The frontend mirror in `static/transit/chunks.js` (`window.transitChunks.aggregate`) is line-for-line the same math — used by `trends-chart.js` for the route comparison chart so client-side and server-side always agree.

**No calendar dependency.** The "scheduled trips" baseline is reconstructed from `transit_stop_times` joined to the `(service_id, date)` pairs the recorder observed running — derived from `transit_stop_delays`, **not** `transit_calendar_dates`. A long-lived prod DB whose GTFS bundle has rolled past the queried date range produces correct numbers anyway, because we trust observation over the published calendar.

**Rebuilding.** `./bin/fetcher chunks` interactively rebuilds chunks for a date range against the live event tables. `./bin/seedtransit` writes synthetic chunks for the dev DB.

The textbook math (`Cv`, `EWTSec`, `WaitMin`, `ComputeSystem`, `ComputeRoutes`) lives in `internal/transit/chunk/math.go` with unit tests in `math_test.go`. SUM-stable identities used:

```
Var(X) = E(X²) − E(X)²              # Cv from headway_sum and headway_sum_sec_sq
EWT    = sum(h²) / (2·sum(h)) − sched_h/2
Wait   = sum(h) / N
```

### Cancellation Incident Detection

Cancellations are grouped into **incidents** by walking the actual schedule for each route and direction:

1. Query all scheduled trips today per (route, headsign), ordered by departure time
2. LEFT JOIN against the latest cancellation feed to mark which trips are cancelled
3. Walk through in order — consecutive cancelled trips form one incident
4. A non-cancelled trip between two cancelled ones breaks the streak into separate incidents

This means 3 back-to-back cancellations on Route 2 inbound = 1 incident (likely one bus/driver went down), while 3 scattered cancellations across the day = 3 separate incidents.

The live map stats bar shows incident counts (not raw trip counts) to present the lowest honest number. Incidents with multiple consecutive trips are flagged explicitly.

### Stop Visit Detection

The vehicle tracker populates `transit_stop_visits` using a two-stage
distance check (see `internal/transit/vehicle_tracker.go`):
- **Threshold:** 50m (calibrated from 22K STOPPED_AT observations: P50=11m, P95=48m)
- **Stage 1 — point distance:** Each 15-second position update checks haversine distance from the fix to every stop on the vehicle's route
- **Stage 2 — segment distance:** If the point is > 50m, `segmentDistToPoint` measures the nearest distance from the stop to the line segment between the previous and current GPS fixes. This catches stops the bus passed between readings (at 50 km/h that's ~200m of unobserved travel per poll). When matched via segment distance, `observed_at` is interpolated along the segment rather than pinned to the latest feed timestamp
- **Dedup:** First sighting per `(trip, stop)` wins via in-memory cache + `ON CONFLICT DO NOTHING`
- **Usage:** Headway, bunching, Cv, and EWT calculations all use `transit_stop_visits.observed_at` timestamps
- **Measurement point:** All delay-based metrics (OTP, avg delay, P90, EWT, headway) are computed exclusively at GTFS time point stops (`transit_stop_times.timepoint = TRUE`). This matches TfL QSI methodology — measuring at schedule adherence checkpoints rather than all stops, which avoids inflating on-time numbers with intermediate stops where small delays average out. For EWT/headway specifically, the busiest time point stop per route is used; falls back to the busiest stop overall if no time point has >= 10 visits.

### Route-Level Comparison

Routes tab offers switchable bar chart comparing all routes by: EWT, OTP, Cancellations, P90, Headway Cv, Bunching Rate. Bars use each route's assigned color.

### Timepoint Schedule

The route detail page shows a schedule grid split by direction (headsign). Each cell shows:
- **Top line** (grey): scheduled departure time
- **Bottom line** (colored): actual arrival time — green (on time), blue (early), purple (left early), red (late)

Timepoint stops are drawn from each direction's representative trip (`timepoint=TRUE` in `transit_stop_times`).

### Not yet computable (would need additional data)
- Passenger-weighted metrics (need ridership/APC data)
- Dwell time (GTFS-RT doesn't provide this directly)
- Crowding/load factor (need APC or occupancy data)
