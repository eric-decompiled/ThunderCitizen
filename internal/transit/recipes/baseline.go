package recipes

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// baselineQuery looks up the frozen scheduled-trip count and the
// scheduled headway for one (route, service_kind, band) from
// transit.route_baseline.
//
// route_baseline is a Tier 2 snapshot built at GTFS load time. It
// answers "what does the published schedule say should happen for this
// route on a typical weekday morning?" — independent of what actually
// happened. The bottle uses these numbers as the denominator for the
// cancellation rate (so a route that runs zero trips on a Sunday
// doesn't accidentally show 0% cancelled because the denominator was
// also zero) and as the SWT term in the EWT formula.
//
// The serviceKind parameter is what makes this recipe queryable as one
// SELECT — without it we'd have to join through stop_delay to discover
// which service_kind applies. The orchestrator calls ServiceKind first
// and threads the result here.
const baselineQuery = `
SELECT
    COALESCE(scheduled_trip_count, 0)::int AS scheduled_count,
    COALESCE(scheduled_headway_sec, 0)::float AS sched_headway_sec
FROM transit.route_baseline
WHERE route_id = $1
  AND service_kind = $2
  AND band = $3
`

// BaselineResult is what one Baseline recipe call returns.
type BaselineResult struct {
	Scheduled  int     // scheduled trip count from route_baseline
	HeadwaySec float64 // scheduled headway in seconds from route_baseline
}

// Baseline returns the scheduled-trip count and the scheduled headway
// for one (route, band, service_kind). serviceKind comes from the
// ServiceKind recipe earlier in the orchestrator. When the route has no
// route_baseline row for the given (kind, band) — e.g. a route that
// doesn't run on Sundays — both fields return zero.
func Baseline(ctx context.Context, db *pgxpool.Pool, routeID, band, serviceKind string) (BaselineResult, error) {
	var r BaselineResult
	if serviceKind == "" {
		// No service_kind means we never observed this route on this date.
		// Don't bother hitting the DB; the bottle will be a zero row.
		return r, nil
	}
	err := db.QueryRow(ctx, baselineQuery,
		routeID,     // $1
		serviceKind, // $2
		band,        // $3
	).Scan(&r.Scheduled, &r.HeadwaySec)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r, nil
		}
		return r, err
	}
	return r, nil
}
