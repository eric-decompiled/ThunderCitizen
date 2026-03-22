package recipes

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// cancelQuery counts cancellations for one (route, date, band) and how
// many of those cancellations were reported with less than 15 minutes of
// notice before the scheduled departure.
//
// Methodology:
//
//   - One cancellation = one distinct (trip_id, start_time, start_date)
//     in transit.cancellation. The recorder may receive multiple feed
//     polls confirming the same cancellation; GROUP BY collapses them.
//   - "Lead time" is the gap between the cancellation's first feed
//     observation and the trip's scheduled departure. Negative leads
//     mean the cancellation was reported AFTER the trip was already
//     supposed to leave (rider showed up, no bus came, then learned
//     it was cancelled).
//   - "No notice" = lead < 15 minutes. The 15-minute threshold matches
//     the industry convention for "did the rider have time to find an
//     alternative".
//
// Band filtering uses the cancellation's start_time string lexicographic
// compare against the band's HH:MM:SS bounds. start_date is matched with
// the GTFS-style "YYYYMMDD" format used in the source feed.
const cancelQuery = `
SELECT
    COUNT(*)::int AS cancelled_count,
    COUNT(*) FILTER (WHERE lead_min < 15)::int AS no_notice_count
FROM (
    SELECT
        EXTRACT(EPOCH FROM (
            (TO_DATE(start_date, 'YYYYMMDD') + start_time::interval)
            - MIN(feed_timestamp)
        )) / 60.0 AS lead_min
    FROM transit.cancellation
    WHERE start_date = TO_CHAR($1::date, 'YYYYMMDD')
      AND route_id = $2
      AND start_time IS NOT NULL
      AND start_time >= $3 AND start_time < $4
    GROUP BY trip_id, start_time, start_date
) c
`

// CancelResult is what one Cancel recipe call returns.
type CancelResult struct {
	Cancelled int // distinct cancelled trips in the band
	NoNotice  int // subset of Cancelled where the lead time was < 15 minutes
}

// Cancel returns the cancellation counts for one chunk. bandStartTime
// and bandEndTime are HH:MM:SS strings (e.g. "06:00:00", "12:00:00") used
// to filter by the cancellation's scheduled departure time.
//
// To compute the cancellation rate from a single chunk: Cancelled * 100 /
// ScheduledCount (which comes from the Baseline recipe). To compute the
// no-notice percentage: NoNotice * 100 / Cancelled.
func Cancel(ctx context.Context, db *pgxpool.Pool, routeID string, date time.Time, bandStartTime, bandEndTime string) (CancelResult, error) {
	var r CancelResult
	err := db.QueryRow(ctx, cancelQuery,
		date,          // $1
		routeID,       // $2
		bandStartTime, // $3 — e.g. "06:00:00"
		bandEndTime,   // $4 — e.g. "12:00:00"
	).Scan(&r.Cancelled, &r.NoNotice)
	return r, err
}
