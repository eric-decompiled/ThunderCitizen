package recipes

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// headwayQuery computes the count, sum, and sum-of-squares of observed
// headway gaps at one route's timepoint stops on one date, filtered to
// one band's hour window.
//
// Everything runs in Thunder Bay local time — the connection's session
// timezone is pinned to America/Thunder_Bay in internal/database/db.go,
// so `observed_at::date` and `EXTRACT(HOUR FROM observed_at)` both speak
// local time without any AT TIME ZONE conversion. Post-midnight visits
// that fall outside the 6am–12am service window are intentionally dropped
// — losing a handful of late-night trips is worth the simpler SQL.
//
// Methodology:
//
//   - A "headway gap" is the seconds between consecutive observations
//     of THIS ROUTE arriving at the same stop. LAG() partitioned by
//     stop_id (not trip_id) so the gap measures rider experience: "how
//     long does the next bus take to show up at this stop?".
//   - Only timepoint stops count, mirroring the OTP recipe's filter.
//     Non-timepoint stops aren't held to a published time.
//   - Headways are clamped to (60, 7200) seconds. Gaps shorter than a
//     minute are GPS noise; gaps longer than two hours are service
//     interruptions that shouldn't pollute a normal headway distribution.
//
// Returns three numbers — count, sum, sum-of-squares — that the chunk
// math (Cv, EWTSec, WaitMin in internal/transit/chunk/math.go) folds
// into Cv via stddev-from-sums and into EWT via the TfL formula.
const headwayQuery = `
WITH headway_pairs AS (
    SELECT
        EXTRACT(EPOCH FROM (
            sv.observed_at - LAG(sv.observed_at) OVER (
                PARTITION BY sv.stop_id
                ORDER BY sv.observed_at
            )
        )) AS headway_sec,
        EXTRACT(HOUR FROM sv.observed_at)::int AS hr
    FROM transit.stop_visit sv
    WHERE sv.route_id = $1
      AND sv.observed_at::date = $2::date
      AND EXISTS (
        SELECT 1 FROM transit.route_pattern_stop rps
        WHERE rps.stop_id = sv.stop_id AND rps.is_timepoint = true
      )
)
SELECT
    COUNT(*)::int AS headway_count,
    COALESCE(SUM(headway_sec), 0)::float AS sum_h,
    COALESCE(SUM(headway_sec * headway_sec), 0)::float AS sum_h_sq
FROM headway_pairs
WHERE hr >= $3 AND hr < $4
  AND headway_sec > 60 AND headway_sec < 7200
`

// HeadwayResult is what one Headway recipe call returns.
type HeadwayResult struct {
	Count    int     // number of valid headway gaps observed in the band
	SumSec   float64 // SUM(h)
	SumSecSq float64 // SUM(h^2)
}

// Headway returns the per-route headway statistics for one chunk.
// startHour and endHour are integer hours in Thunder Bay local time
// (e.g. morning = 6 to 12, exclusive of endHour).
//
// The three sums are deliberately raw — no rates, no averages. The
// downstream chunk package math (Cv, EWTSec, WaitMin) consumes them
// via the stddev-from-sums identity, which lets multiple chunks be
// pooled exactly without recomputing from raw observations.
func Headway(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, startHour, endHour int) (HeadwayResult, error) {
	var r HeadwayResult
	err := db.QueryRow(ctx, headwayQuery,
		routeID,   // $1
		date,      // $2
		startHour, // $3
		endHour,   // $4
	).Scan(&r.Count, &r.SumSec, &r.SumSecSq)
	return r, err
}
