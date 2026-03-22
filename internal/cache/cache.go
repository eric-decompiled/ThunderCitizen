// Package cache defines the small set of HTTP Cache-Control strategies the
// application uses. Every handler that sets Cache-Control should reference
// one of these constants — that way the rules are concentrated in one
// place, easy to grep, and easy to tune as a group.
//
// In non-production environments, internal/middleware.NoCacheInDev wraps
// the response writer and overwrites every Cache-Control header to
// "no-store" right before the first byte ships, so dev never sees stale
// work regardless of which strategy a handler picked.
//
// Strategy guide:
//
//   - Live: realtime polled feeds and SSE streams. Browser must revalidate
//     on every request. Use for the vehicle position stream, the per-stop
//     prediction feed, anything where seeing 15-second-old data is wrong.
//
//   - Short: data that updates within minutes but doesn't need to be
//     revalidated on every keystroke — KPIs, system metrics, vehicle
//     distance, nearby stops, route predictions. 30 seconds is the
//     sweet spot: refresh-spam hits the browser cache, but a fresh tab
//     load picks up new GPS pings.
//
//   - Page: HTML pages and HTMX partials. 5 minutes covers back-nav and
//     refresh-spam without making content updates feel stuck.
//
//   - Reference: GTFS-derived bulk data that only changes when the
//     operator reloads the static feed (routes, stops, route detail).
//     Marked immutable so browsers don't even revalidate — when GTFS
//     reloads we accept the cache lag because the data shape is stable.
//
//   - Static: files under /static/*. internal/assets fingerprints every
//     file at boot and templates emit "?v=<hash>" so changed bytes get a
//     brand-new URL — week-immutable caching is safe across deploys.
package cache

const (
	// Live disables caching at the browser. SSE streams and per-tick
	// realtime endpoints. Use sparingly — every request hits the server.
	Live = "no-cache"

	// Short = 30s. Live-ish data that benefits from coalescing rapid
	// requests but must refresh within a minute or two of real changes.
	// Covers KPI/metric endpoints (cheap reads against the bucket
	// rollup table), vehicle distance, predictions, nearby stops.
	Short = "public, max-age=30"

	// Page = 5 min. HTML pages and HTMX partials. Long enough to absorb
	// refresh spam, short enough that an editor sees their content
	// change within a coffee break.
	Page = "public, max-age=300"

	// Reference = 1 hour, immutable. GTFS-derived reference lists
	// (routes, stops, single-route detail) that only change on operator-
	// triggered feed reloads. Browsers won't revalidate, so accept the
	// cache lag in exchange for fast repeat loads.
	Reference = "public, max-age=3600, immutable"

	// Static = 1 week, immutable. Files served out of /static/*.
	// Cache-busted via internal/assets fingerprints (?v=<hash>) so
	// deploys invalidate by URL change, not by header expiry.
	Static = "public, max-age=604800, immutable"
)
