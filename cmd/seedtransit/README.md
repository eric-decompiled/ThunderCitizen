# seedtransit — dev-only transit metric seeder

Generates synthetic chunks (one per route × day × band) for the dev
database, with a linear bad→good trend over the requested range and a
per-route quality bias to give the data realistic spread. Powers
`/transit/metrics` and `/transit/routes` when working in a fresh dev
environment.

## ⚠️  Dev only

**This tool is NOT bundled into the production Docker image.** The
`Dockerfile` enumerates the binaries it builds explicitly (`server`,
`fetcher`, `summarize`, `auditbudget`, `buildshapes`, `perftest`) and
`seedtransit` is not on that list. Don't add it.

To use the tool, build from source:

```sh
go run ./cmd/seedtransit              # 30 days, seed=42, ending today
go run ./cmd/seedtransit -days 14
go run ./cmd/seedtransit -seed 123
go run ./cmd/seedtransit -end 2026-04-10
go run ./cmd/seedtransit -clean       # delete previously-seeded rows first
```

Or build a binary into the gitignored `bin/`:

```sh
go build -o bin/seedtransit ./cmd/seedtransit
./bin/seedtransit -clean
```

## What it writes

Default run (30 days × 20 routes × 3 bands = 1,800 chunks):

| Table                         | What                                      |
|-------------------------------|-------------------------------------------|
| `transit.route_band_chunk`    | One chunk per (route, date, band). UPSERTed by primary key — re-running is idempotent. |
| `transit.cancellation`        | Synthetic per-trip rows backing the cancel log on the metrics tab. `trip_id` always starts with `seed_` so cleanup is unambiguous. |
| `transit.route`               | Minimal placeholder rows **only** when the table is empty (fresh DB without GTFS loaded). If GTFS is already there, the existing routes are reused untouched. |

Tables it does **not** touch: `stop_delay`, `stop_visit`, `route_baseline`,
`route_pattern_stop`, `scheduled_stop`. The chunk-first read path doesn't
need any of those — chunks are self-contained — and skipping them keeps
the seeder fast and self-sufficient on a fresh DB.

## The trend

Stats improve linearly from "bad" to "good" over the requested range:

| metric        | oldest day | newest day |
|---------------|------------|------------|
| OTP           | ~55%       | ~92%       |
| Cancel rate   | ~6%        | ~0.5%      |
| Headway Cv    | ~0.55      | ~0.15      |
| EWT (derived) | ~4.5 min   | ~0.5 min   |

EWT and Cv are mathematically linked through `EWT = (mean_h / 2) · Cv²`,
so the seeder picks Cv as the primary target and EWT falls out of the
same headway sums. This means the chunk math in
`internal/transit/chunk/math.go` reproduces the seeded numbers exactly
when it reaggregates them — server-rendered KPIs and JS aggregations
both agree with the seed.

Each chunk also gets seeded gaussian noise so consecutive days don't
look perfectly straight.

## Reproducibility

PRNG-seeded; default seed is `42`. **Same seed + same date range +
same route list = byte-identical output.** Re-running with the same
flags overwrites the same rows with the same numbers.

Vary `-seed` to get different noise without changing the overall trend
shape. Vary `-days` to widen or narrow the visible history. Vary `-end`
to "rewind" the dataset to a specific historical day.

## Cleanup

```sh
go run ./cmd/seedtransit -clean              # cleans the default 30-day window
go run ./cmd/seedtransit -clean -days 90     # cleans the last 90 days
```

`-clean` removes:

- All rows in `transit.route_band_chunk` where `date` is in the range.
- Rows in `transit.cancellation` where `trip_id LIKE 'seed_%'` and the
  feed timestamp is in the range.

It does **not** touch `transit.route`, `stop_delay`, `stop_visit`, or
any GTFS-loaded reference data.

## Volumes

Defaults pick ~18 trips per band per route on weekdays, scaled to 75%
on Saturdays and 55% on Sundays, with midday slightly lighter than peak.
Adjust the constants in `seed.go::synthesize` if you need bigger or
smaller numbers — they're at the top of the function with comments.
