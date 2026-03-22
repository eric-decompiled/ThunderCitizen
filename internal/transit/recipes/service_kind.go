package recipes

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// serviceKindQuery picks the service_kind (weekday | saturday | sunday)
// for one (route, date) by reading any observed stop_delay row for that
// pair. The Tier 3 stop_delay table denormalizes service_kind onto every
// row at write time, so we just need any one row to know the kind.
//
// Returns NULL (→ "" in Go) when the route had no observations on that
// date — the orchestrator handles the empty case by writing a zero
// bottle, so the read path can distinguish "we tried to roll up this
// chunk and it was empty" from "this chunk has never been computed".
const serviceKindQuery = `
SELECT MAX(service_kind)
FROM transit.stop_delay
WHERE date = $1::date AND route_id = $2
LIMIT 1
`

// ServiceKind returns the service_kind ("weekday" | "saturday" | "sunday")
// for one (route, date), or "" if the route had no observations.
//
// Used by the orchestrator before calling Baseline, which needs the
// service_kind to look up the right route_baseline row.
func ServiceKind(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time) (string, error) {
	var kind *string
	err := db.QueryRow(ctx, serviceKindQuery, date, routeID).Scan(&kind)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if kind == nil {
		return "", nil
	}
	return *kind, nil
}
