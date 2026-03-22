# Architecture

## Stack

- **Go 1.24+** — Server, templates, background workers
- **templ** — Type-safe HTML templates that compile to Go
- **chi** — HTTP router with middleware support
- **D3.js** — Budget Sankey flow diagrams (loaded from CDN)
- **Leaflet** — Transit live map, ward map with Esri satellite imagery (loaded from CDN)
- **Pico CSS** — Lightweight semantic CSS framework (via npm/Sass)
- **PostgreSQL** — Database with pgx/v5 driver and connection pooling
- **golang-migrate** — Schema migrations (auto-run on startup)
- **log/slog** — Structured logging via `internal/logger`

## Request Flow

```
HTTP Request
    ↓
Middleware (Recoverer → SecureHeaders → RequestLogger)
    ↓
chi Router (cmd/server/main.go)
    ↓
Handler (internal/handlers/ or internal/transit/handler.go)
    ↓
Reporter (internal/transit/reporting.go) — assembles report from event data
    ↓
Repo (internal/transit/repo.go) — database queries
    ↓
View Model (internal/views/) — presentation logic
    ↓
Template (templates/pages/) → Components (templates/components/)
    ↓
HTML Response
```

For transit API endpoints, the handler returns JSON directly from the Reporter.

## Directory Structure

```
cmd/
    server/main.go            Entry point, routing, middleware, server setup
    fetcher/                  Manual CLI: fetcher {budget,gtfs,votes,wards}
    summarize/main.go         LLM motion summarizer
    auditbudget/main.go       Verifies the budget ledger sub-ledgers balance
    buildshapes/main.go       Generates route-shapes.json from GTFS
    gentstypes/main.go        Generates TypeScript interfaces from Go API structs
    perftest/main.go          Hits every server route, prints latency report
patches/
    cmd/cli/                  CLI: patches {extract,apply}
    patches.go                Tracker (Apply, Down, Status), embed FS, drift detection
    *.sql                     Curated data patches (commit these)
internal/
    config/                   Environment configuration
    database/                 PostgreSQL connection pool + health check
    fetch/                    Shared Source struct used by all four fetcher subcommands
    logger/                   Structured logging (log/slog wrapper with component tags)
    httperr/                  Consistent JSON error responses (BadRequest, Internal, etc.)
    middleware/               HTTP middleware (RequestLogger, Recoverer, SecureHeaders)
    handlers/                 HTTP handlers for non-transit pages
    views/                    View models + presentation helpers (budget, councillors, home)
    data/                     Budget types + loader from JSON seed & FIR files; councillor data
    budget/                   FIR fetch library (parse, compute) + ledger store + DiscoverFIRSources/FetchFIR
    council/                  Council minutes scraper, vote parser, DB store + DiscoverVoteSources/FetchVotes
    transit/                  GTFS-RT recording, reporting, metrics + DiscoverGTFSSources/FetchGTFS
    wards/                    Open North ward boundary fetcher (DiscoverSources/Fetch)
templates/
    layout.templ              Base layout with nav, footer
    components/               Reusable Pico CSS components
    pages/                    Page-level templates
static/
    transit/                  Transit JS (map, report, charts), GTFS data, route shapes
    budget/                   Budget Sankey JS, FIR JSON (fir_YYYY.json), seed data (budget_seed.json)
    councillors/              Ward map JS, GeoJSON, photos, minutes PDFs
    css/                      Pico SCSS/CSS
migrations/                   SQL migration files
```

## Component Architecture

### Components (`templates/components/`)

Reusable templ components using semantic HTML with Pico CSS:

| Component | HTML Element | Purpose |
|-----------|--------------|---------|
| `Card`, `LinkedCard` | `<article>` | Content cards |
| `StatGrid` | CSS Grid | Statistics display |
| `PageHeader`, `Hero` | `<header>` | Page headers |
| `AccordionItem` | `<details>/<summary>` | Collapsible sections (animated open/close) |
| `InitialsAvatar` | `<span>` | Avatar with initials |
| `YearSelector` | `<article>/<nav>` | Year navigation (reusable) |
| `TabSelector` | `<nav>` | Tab switching (transit page) |

### Views Layer (`internal/views/`)

View models prepare data for templates:

- **Helpers** — `Initials()`, `CouncillorID()` presentation functions
- **View Models** — Structs with pre-computed display values
- **Sankey Data** — `SankeyData`, `ServiceSankeyJSON` built from budget data

This keeps domain models clean (no presentation methods).

### Data Layer (`internal/data/`)

Budget data loaded from JSON at startup — no hardcoded Go structs.

- `budget.go` — Types (`BudgetContext`, `BudgetItem`, `BudgetYear`), JSON seed loader, FIR loader, revenue merger. `BudgetByYear` map populated via `init()`:
  1. Load `static/budget/budget_seed.json` (editorial years 2022–2026 with highlights, descriptions, meetings)
  2. Load `static/budget/fir_YYYY.json` (FIR years 2011–2023 with service breakdowns)
  3. Merge FIR revenue sources into editorial years for Sankey diagram
- `budget_details.go` — Per-service drill-down data (2026 base). `ServiceDetailsForYear()` scales proportionally for other years.

To update budget numbers: edit `static/budget/budget_seed.json` — no recompilation needed.

### Budget Pipeline (`internal/budget/`)

Fetches Ontario Financial Information Return (FIR) data:

1. Downloads FIR Schedules 10, 12, 40 from Ontario open data (2011–present)
2. Schedule 10: Revenue summary + breakdown by source type
3. Schedule 12: Revenue by function (grants, fees per service)
4. Schedule 40: Expenses by function
5. Computes service categories via `mapping.go`
6. Writes JSON to `static/budget/fir_YYYY.json`

Year range is dynamic — derived from current date, not hardcoded. Driven by `./bin/fetcher budget` (interactive). The server reads `static/budget/fir_*.json` at startup via `internal/data/budget.go` — no DB tables involved.

### Transit Package (`internal/transit/`)

Event-sourced transit analytics. See [docs/transit.md](transit.md) for full details.

**Write path:** Recorder polls 3 GTFS-RT feeds, writes to 4 event tables (`transit_vehicle_positions`, `transit_stop_delays`, `transit_cancellations`, `transit_alerts`).

**Read path:** Reporter assembles reports from event data via windowed SQL queries. No pre-computed snapshots — dashboard data derived on-the-fly.

**Metrics:** System and per-route metrics (EWT, OTP, cancellation rate, headway Cv, bunching) computed from event data. EWT is the primary rider-facing metric. Per-route detail pages show full breakdowns.

**Stop detection:** The vehicle tracker records `transit_stop_visits` rows when a bus is within 50m of a stop on its route — checking both the current GPS fix and the line segment between the previous and current positions (catches stops the bus passed between 15-second readings). Feeds headway/bunching/EWT calculations.

## Data Provenance

Provenance lives in two places:

1. **`data_patch_log`** — append-only audit table that records every Apply and Down of a curated SQL data patch, with the SHA-256 of the patch body at the time of action. Tells you exactly what data shipped, when, and what version of the patch produced it. See [patches/README.md](../patches/README.md).
2. **Source files in `static/`** — `static/budget/fir_YYYY.json`, `static/councillors/votes_*.json`, `static/transit/gtfs/*.txt` etc. are committed (or .gitignored and regenerated via `./bin/fetcher`) and represent the latest fetched state. They're the input to the patch generator (`./bin/patches extract`).

The earlier `documents` + `facts` + `fact_citations` citation graph was removed pre-launch — it was write-only after the CPI removal, and the patches mechanism gives the same audit story for the data we actually ship.

### Two Types of Data — Know Which You're Adding

| Source type | Example | Storage |
|---|---|---|
| **Downloadable file** | FIR XLSX, GTFS ZIP, minutes PDF | Static file under `static/`, then a SQL patch (`./bin/patches extract`) for whatever needs to land in the DB |
| **Live feed / stream** | GTFS-RT protobuf (vehicle positions, delays) | `transit_*` event tables (append-only) — recorded by `internal/transit/recorder.go` |

**Rule**: if you can download it and hash it, it goes through `./bin/fetcher` → static file → patch. If it's a continuous feed, it's events → domain tables → no patch.

### Adding a New Data Source

1. Write a small `Discover*Sources(ctx, opts) ([]fetch.Source, error)` and `Fetch*(ctx, opts) error` pair in the relevant `internal/<package>/` directory (mirror `internal/budget/firsource.go`)
2. Add a subcommand in `cmd/fetcher/<name>.go` that calls Discover → printSources → confirm → Fetch
3. If the data needs to ship to production, add an extractor entry in `patches/cmd/cli/extract.go` so `./bin/patches extract` produces a patch SQL file

## Key Patterns

- **Reporter pattern** — Handler is a thin HTTP adapter; data assembly lives in `Reporter`
- **Event-sourced transit** — Raw events are source of truth; all analytics derived via SQL
- **Static source files** — Budget JSON, council JSON, GTFS .txt: downloaded into `static/`, regenerated via `./bin/fetcher`, committed (or extracted into patches)
- **Structured logging** — `logger.New("component")` → tagged slog output with levels
- **Consistent errors** — `httperr` package for all JSON API error responses
- **Middleware stack** — Panic recovery, security headers, request logging
- **View models** — Handlers build view models, pass to templates; domain models stay pure
- **Component composition** — Templates compose via `@Component(props) { children }`
- **Source attribution** — Patch SQL files carry SHA-256 fingerprints in `data_patch_log`; every apply/down is timestamped
- **Progressive enhancement** — Pages work without JS; Leaflet/D3/HTMX enhance when available
- **Client-side switching** — Budget years and council terms switch via embedded JSON without page reloads
- **Animated interactions** — Accordion open/close, modal entrance/exit, ward map hover states

## Generated Files (do not edit)

- `*_templ.go` — Generated from `.templ` files
- `static/css/style.css` — Compiled from `style.scss` by Sass

## Linting

```bash
make lint       # Run go vet + ESLint
make lint-js    # ESLint only
```
