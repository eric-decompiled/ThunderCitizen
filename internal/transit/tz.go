package transit

import "time"

// TZ is Thunder Bay's time zone (Eastern Time).
var TZ *time.Location

func init() {
	var err error
	TZ, err = time.LoadLocation("America/Thunder_Bay")
	if err != nil {
		// Fallback — should never happen with proper tzdata.
		TZ = time.FixedZone("EST", -5*60*60)
	}
}

// Now returns the current time in Thunder Bay.
func Now() time.Time {
	return time.Now().In(TZ)
}

// Today returns midnight today in Thunder Bay.
func Today() time.Time {
	n := Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, TZ)
}

// ServiceDayCutoffHour is the hour (local time) when the GTFS service day
// rolls over. Before this hour, we're still in the previous day's service.
// Thunder Bay's latest trip ends ~24:23; earliest starts ~06:00. We use 4 AM
// as the boundary — well clear of late-night service spillover.
const ServiceDayCutoffHour = 4

// ServiceDate returns the GTFS service date. Trips scheduled past midnight
// (e.g. 25:30) belong to the previous calendar day's service. Before 4 AM
// local time, we're still in the previous day's service window.
func ServiceDate() time.Time {
	n := Now()
	if n.Hour() < ServiceDayCutoffHour {
		n = n.AddDate(0, 0, -1)
	}
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, TZ)
}

// DateOnly strips the time component, keeping the Thunder Bay timezone.
func DateOnly(t time.Time) time.Time {
	t = t.In(TZ)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, TZ)
}
