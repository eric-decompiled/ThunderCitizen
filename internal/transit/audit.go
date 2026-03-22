package transit

import (
	"context"
	"time"
)

// DivergenceToleranceSec is how far apart the two delay signals can be before
// a cell is considered divergent. 45 seconds picks up the NextLift internal
// update lag (a few seconds) plus our 15s poll phase plus GPS ping jitter,
// without flagging routine skew.
const DivergenceToleranceSec = 45

// AuditTimepoint is one cell in the audit grid: a scheduled timepoint plus
// both delay signals (TripUpdate "obs" and GPS stop_visit "gps") for that
// (trip_id, stop_id). Either side may be nil when no observation exists yet.
type AuditTimepoint struct {
	ScheduledTime string // "HH:MM"
	ObsDelaySec   *int   // from transit.stop_delay.arrival_delay (last-seen feed value)
	GpsDelaySec   *int   // midpoint of (entered_at, exited_at) - scheduled
	GpsEnteredAt  *time.Time
	GpsExitedAt   *time.Time
	DwellSec      *int // exited_at - entered_at in seconds; nil if still in progress
	GpsDriveBy    bool // inside_polls <= 1 → bus transited through the 50m circle without serving
	Divergent     bool // both sides present and |obs - gps| > DivergenceToleranceSec
}

// AuditTrip mirrors TimepointTrip but carries AuditTimepoint cells.
type AuditTrip struct {
	TripID   string
	Headsign string
	Canceled bool
	Stops    []AuditTimepoint
}

// AuditSummary is the per-route roll-up shown at the top of the audit page.
// All counts are over cells that have a scheduled time (empty cells in
// merged directions are excluded).
type AuditSummary struct {
	Total     int // cells with a scheduled time
	BothOK    int // both signals present, within tolerance
	Divergent int // both signals present, outside tolerance
	ObsOnly   int // only the TripUpdate feed fired
	GpsOnly   int // only the GPS interpolation fired
	Neither   int // neither signal yet (early morning, uncovered stops)
}

// AuditSchedule is the audit equivalent of UnifiedSchedule: one grid with
// per-direction sections and one cell per (trip, timepoint) carrying both
// signals.
type AuditSchedule struct {
	Sections []DirectionSection
	Trips    []AuditTrip
	Summary  AuditSummary
}

// AuditIndexViewModel is the data for the audit index page (route chooser).
type AuditIndexViewModel struct {
	Routes  []RouteMetaAPI
	DateISO string // YYYY-MM-DD for the date input default
}

// AuditRouteViewModel is the data for the per-route audit timetable page.
type AuditRouteViewModel struct {
	RouteID   string
	ShortName string
	LongName  string
	Color     string
	TextColor string
	Date      string // "Monday, January 2"
	DateISO   string // YYYY-MM-DD
	Schedule  *AuditSchedule
}

// AuditTimetable assembles an AuditSchedule for one route/date. Unlike the
// public /transit/route/:id page which uses RouteTimepointSchedule (stops
// from a representative trip only), this loader unions every timepoint
// across every trip in each direction so the maintainer sees the widest
// possible grid for comparing obs/gps/Δ/dwell.
func (s *Service) AuditTimetable(ctx context.Context, routeID string, date time.Time) (*AuditSchedule, error) {
	schedules, err := auditRouteTimepointSchedule(ctx, s.db, routeID, date)
	if err != nil {
		return nil, err
	}
	unified := UnifySchedules(schedules)
	if unified == nil || len(unified.Trips) == 0 {
		return &AuditSchedule{}, nil
	}

	gps, err := loadRouteGpsVisits(ctx, s.db, routeID, date)
	if err != nil {
		return nil, err
	}

	return buildAuditSchedule(unified, gps), nil
}

// buildAuditSchedule is pure: takes a UnifiedSchedule and the GPS-visit map
// and returns the audit grid with divergence flags and summary counts.
// Factored out so the conversion can be unit-tested without DB.
func buildAuditSchedule(u *UnifiedSchedule, gps map[string]map[string]gpsVisit) *AuditSchedule {
	sectionStopID := make([]string, 0)
	for _, sec := range u.Sections {
		for _, st := range sec.Stops {
			sectionStopID = append(sectionStopID, st.StopID)
		}
	}

	trips := make([]AuditTrip, 0, len(u.Trips))
	var summary AuditSummary

	for _, t := range u.Trips {
		audit := AuditTrip{
			TripID:   t.TripID,
			Headsign: t.Headsign,
			Canceled: t.Canceled,
			Stops:    make([]AuditTimepoint, len(t.Stops)),
		}
		tripGps := gps[t.TripID]
		for i, cell := range t.Stops {
			audit.Stops[i] = AuditTimepoint{ScheduledTime: cell.ScheduledTime}
			if cell.ScheduledTime == "" {
				continue
			}
			audit.Stops[i].ObsDelaySec = cell.DelaySec

			if i < len(sectionStopID) {
				if v, ok := tripGps[sectionStopID[i]]; ok {
					d := v.DelaySec
					entered := v.EnteredAt
					audit.Stops[i].GpsDelaySec = &d
					audit.Stops[i].GpsEnteredAt = &entered
					audit.Stops[i].GpsExitedAt = v.ExitedAt
					if v.ExitedAt != nil {
						dwell := int(v.ExitedAt.Sub(v.EnteredAt).Seconds())
						audit.Stops[i].DwellSec = &dwell
					}
					if v.InsidePolls != nil && *v.InsidePolls <= 1 {
						audit.Stops[i].GpsDriveBy = true
					}
				}
			}

			if t.Canceled {
				// Canceled trips don't count toward summary.
				continue
			}

			summary.Total++
			obsD := audit.Stops[i].ObsDelaySec
			gpsD := audit.Stops[i].GpsDelaySec
			switch {
			case obsD != nil && gpsD != nil:
				diff := *obsD - *gpsD
				if diff < 0 {
					diff = -diff
				}
				if diff > DivergenceToleranceSec {
					audit.Stops[i].Divergent = true
					summary.Divergent++
				} else {
					summary.BothOK++
				}
			case obsD != nil:
				summary.ObsOnly++
			case gpsD != nil:
				summary.GpsOnly++
			default:
				summary.Neither++
			}
		}
		trips = append(trips, audit)
	}

	return &AuditSchedule{
		Sections: u.Sections,
		Trips:    trips,
		Summary:  summary,
	}
}
