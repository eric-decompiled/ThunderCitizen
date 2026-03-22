package transit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CancelDetail is one cancelled trip in a date range — the unit of work
// for the cancel log on the metrics tab. Distinct from CancelledTrip
// (queries.go) which is "trips cancelled right now from the most recent
// feed poll" and lives on the live page.
type CancelDetail struct {
	Date      string `json:"date"` // YYYY-MM-DD
	RouteID   string `json:"route_id"`
	TripID    string `json:"trip_id"`
	StartTime string `json:"start_time"` // HH:MM (scheduled departure)
	EndTime   string `json:"end_time"`   // HH:MM (scheduled last arrival)
	Headsign  string `json:"headsign"`
	FirstSeen string `json:"first_seen"` // HH:MM in Thunder Bay tz
	LeadMin   int    `json:"lead_min"`   // negative = reported after departure
	LeadLabel string `json:"lead_label"`
}

// LoadCancelDetails returns every cancelled trip whose feed_timestamp falls
// in [from, to) inclusive of the from-side. Used by the metrics tab to
// embed a per-trip cancel log alongside the chunk data — separate from
// the chunk aggregates because each row is a distinct trip, not a sum.
func LoadCancelDetails(ctx context.Context, db *pgxpool.Pool, from, to time.Time) ([]CancelDetail, error) {
	toNext := to.AddDate(0, 0, 1)
	rows, err := db.Query(ctx, `
		SELECT
			TO_CHAR(TO_DATE(start_date, 'YYYYMMDD'), 'YYYY-MM-DD') AS cancel_date,
			route_id, trip_id, start_time,
			COALESCE(scheduled_last_arr_time, '') AS end_time,
			COALESCE(headsign, '') AS headsign,
			TO_CHAR(MIN(feed_timestamp) AT TIME ZONE 'America/Thunder_Bay', 'HH24:MI') AS first_seen,
			EXTRACT(EPOCH FROM (
				(TO_DATE(start_date, 'YYYYMMDD') + start_time::interval)
				- (MIN(feed_timestamp) AT TIME ZONE 'America/Thunder_Bay')
			)) / 60.0 AS lead_min
		FROM transit.cancellation
		WHERE feed_timestamp >= $1 AND feed_timestamp < $2
		  AND start_time IS NOT NULL
		  AND start_date IS NOT NULL
		GROUP BY trip_id, route_id, start_time, start_date, scheduled_last_arr_time, headsign
		ORDER BY start_date, start_time
	`, from, toNext)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CancelDetail
	for rows.Next() {
		var cd CancelDetail
		var leadF float64
		if err := rows.Scan(&cd.Date, &cd.RouteID, &cd.TripID, &cd.StartTime, &cd.EndTime, &cd.Headsign, &cd.FirstSeen, &leadF); err != nil {
			return nil, err
		}
		cd.StartTime = trimTime(cd.StartTime)
		cd.EndTime = trimTime(cd.EndTime)
		cd.LeadMin = int(leadF)
		cd.LeadLabel = leadLabel(cd.LeadMin)
		out = append(out, cd)
	}
	return out, rows.Err()
}
