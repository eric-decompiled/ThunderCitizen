package transit

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/transit/gtfsrt"
)

// Stop is a transit stop with coordinates.
type Stop struct {
	StopID     string   `json:"stop_id"`
	StopName   string   `json:"stop_name"`
	Latitude   float64  `json:"lat"`
	Longitude  float64  `json:"lon"`
	Routes     int      `json:"routes,omitempty"`    // number of routes serving this stop
	RouteIDs   []string `json:"route_ids,omitempty"` // route IDs serving this stop
	Transfer   bool     `json:"transfer,omitempty"`  // official transfer point from GTFS
	IsTerminal bool     `json:"is_terminal,omitempty"`
}

// AllStops returns every stop with valid coordinates.
func AllStops(ctx context.Context, db *pgxpool.Pool) ([]Stop, error) {
	return NewRepo(db).AllStops(ctx)
}

// StopPrediction is an upcoming arrival at a stop.
type StopPrediction struct {
	RouteID       string `json:"route_id"`
	RouteName     string `json:"route_name"`
	Headsign      string `json:"headsign"`
	ScheduledTime string `json:"scheduled"`     // "HH:MM"
	PredictedTime string `json:"predicted"`     // "HH:MM" (adjusted for delay)
	DelaySec      *int   `json:"delay_seconds"` // nil if no real-time data
	MinutesAway   int    `json:"minutes_away"`
	Status        string `json:"status"` // "On time", "2m late", "1m early", "Scheduled"
	RouteColor    string `json:"route_color"`
}

// StopPredictions fetches live GTFS-RT trip updates and returns predicted
// arrivals at the given stop. Uses absolute times from the feed directly
// (no dependency on matching GTFS static trip IDs).
// StopPredictionsResponse wraps predictions with feed metadata.
type StopPredictionsResponse struct {
	Predictions []StopPrediction `json:"predictions"`
	UpdatedAt   *time.Time       `json:"updated_at,omitempty"`
}

func StopPredictions(ctx context.Context, db *pgxpool.Pool, client *Client, stopID string, now time.Time) (StopPredictionsResponse, error) {
	// 1. Fetch the raw GTFS-RT feed (we need the full protobuf, not the parsed version)
	feed, err := client.fetchFeed(ctx, TripFeedPath)
	if err != nil {
		return StopPredictionsResponse{}, fmt.Errorf("fetch trip updates: %w", err)
	}
	feedTS := feedTimestamp(feed)

	// 2. Load route display info from DB
	routeInfo, err := loadRouteDisplayInfo(ctx, db)
	if err != nil {
		return StopPredictionsResponse{}, err
	}

	// 3. Scan all trip updates for StopTimeUpdates matching our stop
	var predictions []StopPrediction

	for _, entity := range feed.Entity {
		tu := entity.TripUpdate
		if tu == nil || tu.Trip == nil {
			continue
		}

		routeID := tu.Trip.GetRouteId()
		if routeID == "" {
			continue
		}

		// Skip canceled trips
		if tu.Trip.ScheduleRelationship != nil {
			sr := *tu.Trip.ScheduleRelationship
			if sr == gtfsrt.TripDescriptor_CANCELED || sr == gtfsrt.TripDescriptor_DELETED {
				continue
			}
		}

		for _, stu := range tu.StopTimeUpdate {
			if stu.GetStopId() != stopID {
				continue
			}

			// Get predicted arrival time (prefer arrival, fall back to departure)
			var predictedTime time.Time
			var delaySec *int
			var scheduledTime time.Time

			if stu.Arrival != nil {
				if stu.Arrival.Time != nil {
					predictedTime = time.Unix(*stu.Arrival.Time, 0)
				}
				if stu.Arrival.Delay != nil {
					d := int(*stu.Arrival.Delay)
					delaySec = &d
					if !predictedTime.IsZero() {
						scheduledTime = predictedTime.Add(-time.Duration(d) * time.Second)
					}
				}
			}
			if predictedTime.IsZero() && stu.Departure != nil {
				if stu.Departure.Time != nil {
					predictedTime = time.Unix(*stu.Departure.Time, 0)
				}
				if stu.Departure.Delay != nil {
					d := int(*stu.Departure.Delay)
					delaySec = &d
					if !predictedTime.IsZero() {
						scheduledTime = predictedTime.Add(-time.Duration(d) * time.Second)
					}
				}
			}

			if predictedTime.IsZero() {
				continue
			}

			// Convert to local time
			predictedLocal := predictedTime.In(now.Location())
			minutesAway := int(predictedLocal.Sub(now).Minutes())

			// Only show upcoming arrivals (allow 2 min in the past)
			if minutesAway < -2 {
				continue
			}

			ri := routeInfo[routeID]

			p := StopPrediction{
				RouteID:       routeID,
				RouteName:     ri.shortName,
				Headsign:      ri.headsign,
				PredictedTime: predictedLocal.Format("3:04 PM"),
				MinutesAway:   minutesAway,
				RouteColor:    ri.color,
			}

			if !scheduledTime.IsZero() {
				p.ScheduledTime = scheduledTime.In(now.Location()).Format("3:04 PM")
			} else {
				p.ScheduledTime = p.PredictedTime
			}
			p.DelaySec = delaySec

			if delaySec == nil {
				p.Status = "Scheduled"
			} else if *delaySec >= -60 && *delaySec <= 60 {
				p.Status = "On time"
			} else if *delaySec > 60 {
				p.Status = fmt.Sprintf("%dm late", *delaySec/60)
			} else {
				p.Status = fmt.Sprintf("%dm early", -*delaySec/60)
			}

			predictions = append(predictions, p)
		}
	}

	// Sort by predicted arrival
	sort.Slice(predictions, func(i, j int) bool {
		return predictions[i].MinutesAway < predictions[j].MinutesAway
	})

	if len(predictions) > 12 {
		predictions = predictions[:12]
	}

	resp := StopPredictionsResponse{Predictions: predictions}
	if !feedTS.IsZero() {
		resp.UpdatedAt = &feedTS
	}
	return resp, nil
}

type routeDisplay struct {
	shortName string
	color     string
	headsign  string
}

func loadRouteDisplayInfo(ctx context.Context, db *pgxpool.Pool) (map[string]routeDisplay, error) {
	repo := NewRepo(db)
	rows, err := repo.RouteDisplayInfo(ctx)
	if err != nil {
		return nil, err
	}

	info := make(map[string]routeDisplay, len(rows))
	for _, r := range rows {
		info[r.RouteID] = routeDisplay{shortName: r.ShortName, color: r.Color, headsign: r.LongName}
	}
	return info, nil
}

func formatHM(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if h >= 24 {
		h -= 24
	}
	return fmt.Sprintf("%d:%02d", h, m)
}
