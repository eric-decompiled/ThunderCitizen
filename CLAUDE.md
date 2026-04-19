# CLAUDE.md

## Commands

```bash
go test ./...             # All tests
make dev                  # Run with hot reload (auto-applies migrations + data patches on boot)
make all                  # Build every helper binary into bin/ (gitignored)
./scripts/backup.sh --dev     # Gzipped pg_dump via docker exec → ./backups/
templ generate            # Regenerate templ → Go
npm run css               # Rebuild SCSS → CSS
npm run build:js          # Rebuild TS → JS (leaflet shim)
./bin/fetcher             # Interactive: refresh source data (budget/gtfs/votes/wards)
make muni-extract         # Dev DB → signed TSV bundle in data/muni (applied on server boot)
make muni-publish         # data/muni → zip + upload to DO Spaces
go run ./cmd/perftest     # Latency report (server must be running)
go run ./cmd/perftest -r  # Record + delta vs last run (saves to perftest/)
```

## Key Patterns

- **Route → Handler → ViewModel → Template** (all pages)
- **pgx/v5** plain SQL, no ORM
- **templ** templates compile to Go — do not edit `*_templ.go`
- **Pico CSS** with Sass — do not edit `static/css/style.css`. `style.scss` is a coordinator that `@use`s partials: edit the appropriate one (`_tokens.scss`, `_mixins.scss`, `_placeholders.scss`, `_budget.scss`, `_transit.scss`, `_council.scss`)
- **Static source → signed muni bundle** for curated data (councillors, budget, council votes, wards). Fetchers in `cmd/fetcher` write `static/*.json` → `./bin/muni extract` emits TSVs + `BOD.tsv` under `data/muni/` → `./bin/munisign sign` + `./bin/muni publish` zips and uploads to DO Spaces. On boot the server downloads the signed bundle, verifies the signature, and applies any new datasets via `internal/muni/apply.go` — tracked per-dataset in `data_patch_log` (checksum + signer), throttled by `muni_fetch_state.last_checked_at` (24h). No manual seed step.
- **Append-only `transit_*` event tables** for GTFS-RT data (recorder writes, everything else reads via SQL)
- **Standalone Go scripts** live in `cmd/` (e.g. `cmd/buildshapes`, `cmd/gentstypes`), not `scripts/`

## Shared Map Component

Both Transit and Council pages use Leaflet maps via a shared templ component in `templates/components/map.templ`.

### `LeafletMap(MapProps)`
Renders: Leaflet CDN, `.map-wrap` container with tile data attributes, map div, children slot, page scripts. Shared behavior (scroll-zoom-on-click, zoom control positioning to bottom-left, `.map-active` focus ring) is handled by an embedded script that finds the Leaflet instance after page JS creates it.

```go
type MapProps struct {
    ID        string          // "transit-map", "ward-map"
    AriaLabel string
    Title     string          // compact header title (renders .map-header bar)
    Layers    []MapLayerGroup // layer toggle buttons in header
    TileLight TilePreset      // TileVoyager, TileStadiaDark, TileEsriSatellite
    TileDark  TilePreset      // 0 = same as TileLight
    Class     string          // extra CSS class ("transit-map-wrap")
    Scripts   []string        // JS loaded after Leaflet
}
```

- **Title + Layers** → renders a `.map-header` bar above the map (used by ward map). Transit uses its own `terminal-map-header` instead.
- **`MapLayerBar([]MapLayerGroup)`** — renders `<button data-layer="key">` toggles. Page JS reads `.active` class for initial state and wires click handlers.
- **Tile presets** — `TileVoyager` (CartoDB street), `TileStadiaDark` (dark street), `TileEsriSatellite`. URLs/attribution rendered as `data-tile-*` attributes; page JS reads them to create `L.tileLayer`.
- **Page JS owns `L.map()` init** — each page configures its own Leaflet options (transit disables default zoom/attribution; ward uses defaults). The shared component doesn't call `L.map()`.

### Consumer patterns

**Transit** (`transit.templ` + `web/transit/transit-map.ts`): No Title/Layers in MapProps (has custom terminal header). Layer bar rendered via `MapLayerBar(transitLayerGroups())` in the page's own header. Children = info bar, status bar, trip planner overlay.

**Ward** (`councillors.templ` + `static/councillors/ward-map.js`): Uses Title="Ward Map" + Layers for the Wards toggle. GeoJSON boundaries with per-ward colors, hover/click info bar, permanent labels.

## Color Theming

**Terminal aesthetic site-wide.** Solarized cream light mode + green phosphor dark mode. All theme colors via CSS custom properties — never hardcode hex for theme decisions. Use `color-mix()` for tinted backgrounds (badges, alerts, motion cards).

### Palettes

**Light (Fractured Stone — cool/ash):** three-tier stone hierarchy — page `#e1d9c9` (light stone), card body `#cfc7b5` (mid stone), nav/strip/footer `#beb5a1` (deep stone). Solarized blue `#155a8a` for interactive affordances (links, buttons, focus) and deeper `#0b4670` for headings/terminal labels — both darkened from prior cream-palette values so AA holds on the deeper surfaces. Muted text (`--term-fg-dim`) is `#2f4550` — darkened so it reaches 4.9:1 on the deep-stone strip (footer, card footer, panel bars), not just the mid-stone card body.

**Dark (Green Phosphor):** near-black green bg `#0d1a0d` (page), `#141e14` (card, lifted above page per Material-Dark elevation convention), `#0a100a` (strip/nav/footer, deepest). Phosphor green text `#4ade80` (10.3:1). Nav + site footer carry a phosphor-green halo box-shadow (`--strip-edge-glow-below/-above`) so they frame visibly against the near-black tiers. Headings collapse onto `--accent` — single-phosphor CRT vibe. CRT scanlines on header, green glow on title.

Dark mode is defined via `@mixin dark-theme` applied to both `@media (prefers-color-scheme: dark)` and `:root[data-theme="dark"]`. Console helper: `toggleTheme()` / `toggleTheme("dark")` / `toggleTheme("light")`.

### CSS variables (`:root` in `static/css/_tokens.scss`)

| Variable | Light | Dark | Purpose |
|----------|-------|------|---------|
| `--thunder-900` to `--thunder-50` | Solarized grey scale | Green phosphor scale | Text/bg hierarchy (flips) |
| `--accent` | `#4a6100` (olive green) | `#4ade80` (phosphor) | Interactive affordances: links, buttons, focus rings |
| `--heading` | `#155a8a` (Solarized blue, darkened) | `var(--accent)` — phosphor green | h1/h2/h3 + terminal-label family. Light uses classic Solarized blue; dark collapses onto --accent so the green-phosphor CRT look stays unified |
| `--heading-glow` | `none` | phosphor bloom | Heading text-shadow. Off in light; `0 0 6px rgba(74,222,128,0.35)` in dark for the CRT glow |
| `--heading-warm` | `#7a3f0a` (darkened Solarized orange) | `var(--accent)` — phosphor green | Scoped warm-accent variant. Applied via `.home article > header` to give home-page card header bars a rust/amber tone against the beige strip. Dark mode collapses onto --accent so the phosphor look stays unified |
| `--term-*` tokens | Solarized values | Phosphor values | Semantic terminal tokens (bg, fg, border, glow) |
| `--surface-dark` | `#002b36` | `#0a100a` | Header/footer background |
| `--status-ok/warn/error/info/early-dep/muted` | Darkened for AA on cream | Bright for dark bg | Status semantics |
| `--proposal-1/2/3` | Stable | Stable | Proposal accent colors |

### Typography

**Mono-only.** There is a single type face site-wide — `--font-mono`. `--font-prose` is an alias that resolves to `--font-mono`, so existing `font-family: var(--font-prose)` declarations (e.g. `.lead`, `.motion-text`, `.motion-heading`, `.motion-agenda-item`, `.sankey-detail-body p`, `.councillor-bio`) continue to work but render in mono. Don't add new `--font-prose` consumers; new code should inherit the default mono or omit `font-family` entirely.

**Mono stack** covers Apple (SF Mono, Menlo) → Windows 11 (Cascadia Mono) → Windows (Consolas) → Android (Roboto Mono) → Ubuntu (Ubuntu Mono) → other Linux (DejaVu Sans Mono) → older Windows (Courier New) → generic `monospace`. Full stack in `static/css/_tokens.scss`.

**Headings** are terminal labels: all `0.72rem`, uppercase, `letter-spacing: 0.08em`, `color: var(--heading)` (Solarized blue in light, phosphor green in dark). Weight is the hierarchy lever: h1=800, h2=700, h3=600. In dark mode a subtle phosphor text-shadow is applied via `--heading-glow` (no-op in light). `--accent` is reserved for interactive affordances (links, buttons, focus) — never use it for heading text.

### Accessing theme colors

- **SCSS**: `color: var(--status-error);` or `background: color-mix(in srgb, var(--status-error) 15%, var(--thunder-50));`
- **Templates**: Use `.text-status-ok`, `.text-status-error` etc. utility classes. For inline styles: `style="border-left-color:var(--proposal-1)"`.
- **Vanilla JS**: `var tc = ThemeColors();` then `tc.statusOk`, `tc.accent`, `tc.termAccent`, etc. (`static/js/theme-colors.js` loaded globally)
- **TypeScript**: `import { readThemeColors } from "../theme-colors";` then call after DOMContentLoaded

### Phosphor Pills

All colored pill/badge/label elements use the **phosphor pill system** — two Sass mixins that emit `--badge-*` CSS tokens consumed by `%badge-base` (in `_placeholders.scss`).

**Light mode** (`badge-light-hue($hue)`) — ward-map aesthetic: washed tinted fill (22% hue on cream), solid hue border, darkened hue text, no glow.

**Dark mode** (`badge-dark-hue($hue)`) — CRT aesthetic: dark near-black fill (14% hue tint), saturated phosphor text + border, drop-shadow glow, scanline overlay.

**To add a new pill variant:**
1. Component SCSS: `@extend %badge-base; @include badge-light-hue(#hex);`
2. Add dark override to `@mixin badge-dark-overrides` in `style.scss`: `@include badge-dark-hue(#hex);`

Existing consumers: `.badge-*` (result/significance/term), ward subtitle badges, `.motion-filter-pill--*` (active state). Full docs in `_mixins.scss`.

### What NOT to tokenize
- **Route identity colors** (ROUTE_COLORS maps) — GTFS data, not theme. Also used for Sankey budget nodes.
- **Ward identity colors** — data
- **Term badge colors** (belt progression) — domain data with manual dark mode overrides
- **HSL interpolations** (delay ring gradient) — computed, not a token

## Transit Page UI

- **Tab order**: Live, Metrics, Routes, Method
- **Metrics tab** has 6 KPI cards in a 3×2 grid, a trend chart (click card to switch KPI), and a route comparison bar chart
- **KPI card convention**: main value in `.kpi-value`, three sub-slots showing Morning/Midday/Evening breakdown. Server-rendered via `KPIFromChunks(vm.Chunks, metric, band)` in `view_helpers.go`
- **6 metrics** (ordered simplest→hardest, matching Method tab): OTP, Cancellation Rate, Cancel Notice, Stop Wait, EWT, Headway Cv
- **Time bands** — three 6-hour windows defined once in `metrics.go:Bands`: **Morning** (6–12), **Midday** (12–18), **Evening** (18–24). Hours outside 6–24 are excluded — Thunder Bay Transit doesn't run before 6am.
- **Chunk model** — the metric unit is one **chunk**: 1 route × 1 day × 1 band, persisted as one row in `transit.route_band_chunk` (migration `000003`). Each chunk stores raw counts and SUM-stable headway sums (`headway_sum_sec`, `headway_sum_sec_sq`, `sched_headway_sec`), so any aggregation across routes/days/bands is exact arithmetic — never an average of percentages.
- **Recipes (write path)** — `BuildChunksForDate` in `internal/transit/chunk.go` runs five small per-metric SQL queries from `internal/transit/recipes/` (`service_kind`, `otp`, `cancel`, `baseline`, `headway`), one per chunk, then upserts the assembled `chunk.Chunk`. Each recipe is its own file with a SQL constant and a Go function so the formulas can be audited in isolation. The chunk math itself (`Cv`, `EWTSec`, `WaitMin`, `ComputeSystem`, `ComputeRoutes`) lives in `internal/transit/chunk/` with textbook unit tests.
- **Read path** — `Service.Chunks(ctx, from, to)` returns `[]chunk.ChunkView` from `ChunkCache` (`chunk_cache.go`), which lazy-loads from `transit.route_band_chunk` and caches forever per (route, date, band) — `today` is the only key allowed to refresh, everything else is immutable history.
- **Aggregation** — `KPIFromChunks` and `RouteRowKPIFromChunks` in `view_helpers.go` route through `chunk.KPI` (`internal/transit/chunk/rollup.go`). For OTP, cancellation rate, cancel notice, and wait it sums raw counts across the slice and divides once. **Cv and EWT are different**: they pool headway sums per route, compute the metric per route, then take an unweighted arithmetic mean across routes (each route weighted equally regardless of trip volume) — necessary because Cv/EWT are nonlinear in the underlying sums. Empty band string (`""`) pools all three bands. The mirror frontend port is `static/transit/chunks.js` (`window.transitChunks.aggregate`) — same formulas and split, used by `trends-chart.js` for the route comparison chart.
- **No KPI endpoint** — KPIs are server-rendered into the page via `KPIFromChunks` and the chunks themselves are embedded via `@templ.JSONScript("transit-chunks", vm.Chunks)` for client-side aggregation. There is no `/api/transit/kpis` or `/api/transit/chunks` — the chunks data only travels with the page.
- **Auto-rollup** — `internal/transit/chunk_rollup.go` runs in a goroutine wired in `cmd/server/main.go` next to the recorder. On boot it backfills any date in the last 60 days where events exist but chunks don't; then every 10 min it rebuilds today's chunks. Idempotent upserts. Without this, `route_band_chunk` stays empty and every KPI renders blank — prod hit exactly this before the rollup existed; dev masked it because `seedtransit` pre-fills synthetic chunks.
- **Manual rebuilds** — `./bin/fetcher chunks` interactively rebuilds chunks for a date range (use after changing a recipe or to fill deeper than the auto-backfill window). `./bin/seedtransit` writes synthetic chunks for the dev DB when GTFS hasn't been loaded.
- **Cache layer** — non-chunk cached data products live in `RepoCache` (`repo_cache.go`) as `CacheSlot[T]` / `CacheMap[K,V]` fields, with double-checked-locking lazy-load primitives in `cache.go`. The `live` slot is the only one with a TTL (30s via `NewCacheSlotTTL`); everything else caches forever. Chunks live in their own `ChunkCache`, not in `RepoCache`.
- **Browser cache-control** — Five named strategies in `internal/cache/cache.go`: `Live` (`no-cache`, SSE/realtime feeds), `Short` (30s, predictions/distance/nearby stops), `Page` (5 min, HTML pages and search), `Reference` (1h immutable, GTFS-derived bulk data like routes/stops), `Static` (1 week immutable, `/static/*`). Every handler that sets `Cache-Control` references one of these constants — grep `cache.Live`, `cache.Short`, etc. In non-production environments, `middleware.NoCacheInDev` (wired in `cmd/server/main.go`) wraps the response writer and overwrites every Cache-Control to `no-store` right before the first byte ships, so dev never sees stale work regardless of which strategy a handler picked. In production it's a no-op.
- **pgx query mode** — `DefaultQueryExecMode = QueryExecModeCacheDescribe` in `database/db.go`. Cache the parameter type descriptions but re-plan every query; the default `CacheStatement` switches to a Postgres generic plan after 5 executions and picks a pathological join order for the recipe queries.
- **Stop visit detection** uses line-segment interpolation between consecutive GPS positions (`segmentDistToPoint` in `vehicle_tracker.go`), not just point proximity. Catches stops the bus passed between 15-second GPS readings
- **Route finder** is an accordion overlay pinned to the top-right of the map (`trip-planner-overlay`). Collapsed = tab, expanded = form + results panel with fixed 380px height
- **Form layout** uses `display: table` inside the overlay body — labels as tight left cells, inputs fill the right
- **Cancellation badges** on route pills split into two: red "X upcoming" and gray "Y earlier". Stat bar matches the same split using `upcomingCancelledTrips` / `pastCancelledTrips` (both count trips, not incidents)
- **Stop predictions API** returns `{ predictions: [...], updated_at }` — the `updated_at` is the GTFS-RT feed timestamp, shown as "Updated Xs ago" in stop popups
- **Stop hover** on map enlarges the marker (+3 radius) and shows a tooltip with the stop name
- **Skeleton loading** — route grid shows pulsing pill shapes, live stats show skeleton text blocks (`.skeleton` / `.skeleton-text` / `.skeleton-pill` classes with `skeleton-pulse` animation)
- **Map container** uses shared `LeafletMap` with `Class: "transit-map-wrap"` for terminal theming. Transit's custom `terminal-map-header` sits above it with title, layer bar, and Features controls.
- **Zoom buttons** — shared component positions them bottom-left. Pico CSS overrides ensure `+` has rounded top, `-` has rounded bottom

## Sticky Table Headers

Tables with data that scrolls beyond the viewport use sticky headers with a glass-effect bar (`backdrop-filter: blur`, `--glass-bg`, `--glass-border`). Two patterns depending on whether the table is inside a horizontal scroll container:

### Pattern 1: CSS-only (no overflow container)

For tables not inside `overflow-x: auto`, sticky works directly on `<thead>` or `<th>`:

```scss
thead {
  position: sticky;
  top: 2.1rem; // clear sticky nav
  z-index: 2;
}
th {
  background: var(--glass-bg);
  backdrop-filter: blur(6px);
  border-bottom: 1px solid var(--glass-border);
}
```

Requires `border-collapse: separate; border-spacing: 0` on the table. Parent containers must not have `overflow: hidden` (use `overflow: clip` instead if clipping is needed for scanlines/rounded corners).

**Used by:** Route directory table (`.route-table` in `transit.templ`)

### Pattern 2: Extracted header + JS sync (inside overflow container)

When the table is inside `overflow-x: auto` (for horizontal scrolling), `position: sticky` can't reach the viewport. Extract the header into a separate sticky element above the scroll container:

1. **Template**: Render header twice via a shared sub-template — once in a `.sticky-header` div above the scroll container, once as a hidden `<thead>` inside the table (for column sizing + a11y)
2. **CSS**: `.sticky-header` gets `position: sticky; top: 2.1rem` + glass effect. Original `<thead>` gets `visibility: collapse`
3. **JS**: Sync column widths from the hidden thead to the clone, and sync `scrollLeft` on the scroll container's `scroll` event. Re-run on `htmx:afterSwap` for dynamic content

**Used by:** Route timetable (`.route-tp-sticky-header` in `route.templ`), vote matrix photo bar (`.vote-matrix-photo-bar` in `councillors.templ`)

### Gotchas
- `overflow: hidden` on any ancestor kills sticky — switch to `overflow: clip`
- `overflow-x: auto` implicitly sets `overflow-y: auto`; add `overflow-y: hidden` if vertical scroll is unwanted
- The article scanline rule (`article > *:not(.sr-only)`) sets `position: relative` on direct children — exclude sticky elements via `:not(.your-sticky-class)`
- `top: 2.1rem` assumes the site's sticky nav height; adjust if nav changes

## Docs

- [docs/architecture.md](docs/architecture.md) - Stack, request flow, data provenance
- [docs/development.md](docs/development.md) - Local setup and commands
- [docs/database.md](docs/database.md) - Schema, PostGIS, indexes, connection pooling
- [docs/docker.md](docs/docker.md) - Docker Compose services and commands
- [docs/transit.md](docs/transit.md) - GTFS-RT feeds, recorder, trip planner (RAPTOR), PostGIS
- [docs/transit-metrics.md](docs/transit-metrics.md) - Performance KPIs, methodology, incident detection
- [docs/council.md](docs/council.md) - Council minutes scraping, vote parsing
- [docs/summarize-motions.md](docs/summarize-motions.md) - LLM motion classification runbook
- [docs/data-visualization.md](docs/data-visualization.md) - Chart selection and principles
- [docs/accessibility.md](docs/accessibility.md) - WCAG 2.2 AA targets and compliance notes
- [cmd/fetcher/README.md](cmd/fetcher/README.md) - Manual fetcher CLI and programmatic API
- [cmd/seedtransit/README.md](cmd/seedtransit/README.md) - Synthetic transit chunks for dev
- [DEPLOY.md](DEPLOY.md) - Production deployment runbook (App Platform)
- [DEPLOY-DROPLET.md](DEPLOY-DROPLET.md) - Alternative deployment runbook (single droplet + Caddy)
