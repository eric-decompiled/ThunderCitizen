package transit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "google.golang.org/protobuf/proto"

	"thundercitizen/internal/transit/gtfsrt"
)

const (
	defaultBaseURL  = "http://api.nextlift.ca/gtfs-realtime"
	defaultTimeout  = 10 * time.Second
	VehicleFeedPath = "/vehicleupdates.pb"
	TripFeedPath    = "/tripupdates.pb"
	AlertFeedPath   = "/alerts.pb"
)

// Client fetches and parses GTFS-RT feeds from the NextLift API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a GTFS-RT client with the default NextLift base URL.
func NewClient() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// VehiclePosition is a parsed position from the GTFS-RT feed (in-memory only, not stored in DB).
type VehiclePosition struct {
	FeedTimestamp time.Time
	VehicleID     string
	RouteID       *string
	TripID        *string
	Latitude      float64
	Longitude     float64
	Bearing       *float32
	Speed         *float32
	StopStatus    *string
	CurrentStopID *string
}

// VehicleFeed is a parsed snapshot of the vehicle positions feed.
type VehicleFeed struct {
	Timestamp time.Time
	Positions []VehiclePosition
}

// DelayObservation is a per-stop delay measurement from a trip update.
type DelayObservation struct {
	TripID         string
	RouteID        string
	StopID         string
	StopSequence   *int32
	ArrivalDelay   *int32
	DepartureDelay *int32
}

// TripFeed is a parsed snapshot of the trip updates feed.
type TripFeed struct {
	Timestamp     time.Time
	Delays        []DelayObservation
	Cancellations []TripCancellation
}

// AlertFeed is a parsed snapshot of the service alerts feed.
type AlertFeed struct {
	Timestamp    time.Time
	Observations []AlertObservation
}

// FetchVehicles fetches and parses the vehicle positions feed.
func (c *Client) FetchVehicles(ctx context.Context) (*VehicleFeed, error) {
	feed, err := c.fetchFeed(ctx, VehicleFeedPath)
	if err != nil {
		return nil, err
	}

	feedTS := feedTimestamp(feed)
	var positions []VehiclePosition

	for _, entity := range feed.Entity {
		v := entity.Vehicle
		if v == nil || v.Position == nil {
			continue
		}

		pos := VehiclePosition{
			FeedTimestamp: feedTS,
			Latitude:      float64(v.Position.GetLatitude()),
			Longitude:     float64(v.Position.GetLongitude()),
		}

		if v.Vehicle != nil {
			if v.Vehicle.Label != nil {
				pos.VehicleID = *v.Vehicle.Label
			} else if v.Vehicle.Id != nil {
				pos.VehicleID = *v.Vehicle.Id
			}
		}
		if pos.VehicleID == "" {
			pos.VehicleID = entity.GetId()
		}

		if v.Trip != nil {
			pos.RouteID = v.Trip.RouteId
			pos.TripID = v.Trip.TripId
		}
		if v.Position.Bearing != nil {
			pos.Bearing = v.Position.Bearing
		}
		if v.Position.Speed != nil {
			pos.Speed = v.Position.Speed
		}
		if v.CurrentStatus != nil {
			s := v.CurrentStatus.String()
			pos.StopStatus = &s
		}
		if v.StopId != nil {
			pos.CurrentStopID = v.StopId
		}

		positions = append(positions, pos)
	}

	return &VehicleFeed{Timestamp: feedTS, Positions: positions}, nil
}

// FetchTrips fetches and parses the trip updates feed.
func (c *Client) FetchTrips(ctx context.Context) (*TripFeed, error) {
	feed, err := c.fetchFeed(ctx, TripFeedPath)
	if err != nil {
		return nil, err
	}

	feedTS := feedTimestamp(feed)
	var delays []DelayObservation
	var cancellations []TripCancellation

	for _, entity := range feed.Entity {
		tu := entity.TripUpdate
		if tu == nil || tu.Trip == nil {
			continue
		}

		routeID := tu.Trip.GetRouteId()
		tripID := tu.Trip.GetTripId()
		if routeID == "" || tripID == "" {
			continue
		}

		// Trip-level cancellations
		if tu.Trip.ScheduleRelationship != nil {
			sr := tu.Trip.ScheduleRelationship
			switch *sr {
			case gtfsrt.TripDescriptor_CANCELED, gtfsrt.TripDescriptor_DELETED:
				cancellations = append(cancellations, TripCancellation{
					FeedTimestamp:        feedTS,
					TripID:               tripID,
					RouteID:              routeID,
					StartDate:            tu.Trip.StartDate,
					StartTime:            tu.Trip.StartTime,
					ScheduleRelationship: sr.String(),
				})
				continue
			}
		}

		for _, stu := range tu.StopTimeUpdate {
			stopID := stu.GetStopId()
			if stopID == "" {
				continue
			}

			obs := DelayObservation{
				TripID:  tripID,
				RouteID: routeID,
				StopID:  stopID,
			}
			if stu.StopSequence != nil {
				seq := int32(*stu.StopSequence)
				obs.StopSequence = &seq
			}
			if stu.Arrival != nil && stu.Arrival.Delay != nil {
				obs.ArrivalDelay = stu.Arrival.Delay
			}
			if stu.Departure != nil && stu.Departure.Delay != nil {
				obs.DepartureDelay = stu.Departure.Delay
			}

			delays = append(delays, obs)
		}
	}

	return &TripFeed{Timestamp: feedTS, Delays: delays, Cancellations: cancellations}, nil
}

// FetchAlerts fetches and parses the service alerts feed.
func (c *Client) FetchAlerts(ctx context.Context) (*AlertFeed, error) {
	feed, err := c.fetchFeed(ctx, AlertFeedPath)
	if err != nil {
		return nil, err
	}

	feedTS := feedTimestamp(feed)
	var observations []AlertObservation

	for _, entity := range feed.Entity {
		a := entity.Alert
		if a == nil {
			continue
		}

		alertID := entity.GetId()
		if alertID == "" {
			continue
		}

		obs := AlertObservation{
			FeedTimestamp: feedTS,
			AlertID:       alertID,
		}

		if a.Cause != nil {
			s := a.Cause.String()
			obs.Cause = &s
		}
		if a.Effect != nil {
			s := a.Effect.String()
			obs.Effect = &s
		}
		if a.HeaderText != nil && len(a.HeaderText.Translation) > 0 {
			obs.Header = a.HeaderText.Translation[0].Text
		}
		if a.DescriptionText != nil && len(a.DescriptionText.Translation) > 0 {
			obs.Description = a.DescriptionText.Translation[0].Text
		}
		if a.SeverityLevel != nil {
			s := a.SeverityLevel.String()
			obs.SeverityLevel = &s
		}
		if a.Url != nil && len(a.Url.Translation) > 0 {
			obs.URL = a.Url.Translation[0].Text
		}
		if len(a.ActivePeriod) > 0 {
			period := a.ActivePeriod[0]
			if period.Start != nil {
				t := time.Unix(int64(*period.Start), 0).UTC()
				obs.ActiveStart = &t
			}
			if period.End != nil {
				t := time.Unix(int64(*period.End), 0).UTC()
				obs.ActiveEnd = &t
			}
		}

		for _, ie := range a.InformedEntity {
			if ie.RouteId != nil {
				obs.AffectedRoutes = append(obs.AffectedRoutes, *ie.RouteId)
			}
			if ie.StopId != nil {
				obs.AffectedStops = append(obs.AffectedStops, *ie.StopId)
			}
		}

		observations = append(observations, obs)
	}

	return &AlertFeed{Timestamp: feedTS, Observations: observations}, nil
}

// VehicleWithDelay is a vehicle position merged with its trip delay.
type VehicleWithDelay struct {
	ID       string  `json:"id"`
	RouteID  string  `json:"route_id"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Bearing  float32 `json:"bearing"`
	Speed    float32 `json:"speed"`
	Status   string  `json:"status"`
	StopID   string  `json:"stop_id,omitempty"`   // current/next stop ID
	NearStop string  `json:"near_stop,omitempty"` // nearest stop name (resolved server-side)
	Delay    *int32  `json:"delay"`               // seconds; nil = unknown
}

// ShortVehicleID strips the common transit agency prefix and leading zeros
// from a vehicle ID. e.g. "3420100177" → "177"
func ShortVehicleID(id string) string {
	const prefix = "34201"
	if strings.HasPrefix(id, prefix) {
		id = id[len(prefix):]
	}
	id = strings.TrimLeft(id, "0")
	if id == "" {
		return "0"
	}
	return id
}

// FetchVehiclesWithDelay fetches both the vehicle positions and trip updates
// feeds, then merges them by trip_id to attach delay to each vehicle.
func (c *Client) FetchVehiclesWithDelay(ctx context.Context) ([]VehicleWithDelay, time.Time, error) {
	vFeed, err := c.FetchVehicles(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}

	// Build trip delay map from trip updates feed
	tripDelay := map[string]int32{}
	if tFeed, err := c.FetchTrips(ctx); err == nil {
		// Use per-stop delays: take the last reported delay per trip
		perTrip := map[string]int32{}
		for _, d := range tFeed.Delays {
			if d.ArrivalDelay != nil {
				perTrip[d.TripID] = *d.ArrivalDelay
			} else if d.DepartureDelay != nil {
				perTrip[d.TripID] = *d.DepartureDelay
			}
		}
		tripDelay = perTrip
	}

	var result []VehicleWithDelay
	for _, v := range vFeed.Positions {
		vwd := VehicleWithDelay{
			ID:  ShortVehicleID(v.VehicleID),
			Lat: v.Latitude,
			Lon: v.Longitude,
		}
		if v.RouteID != nil {
			vwd.RouteID = *v.RouteID
		}
		if v.Bearing != nil {
			vwd.Bearing = *v.Bearing
		}
		if v.Speed != nil {
			vwd.Speed = *v.Speed
		}
		if v.StopStatus != nil {
			vwd.Status = *v.StopStatus
		}
		if v.CurrentStopID != nil {
			vwd.StopID = *v.CurrentStopID
		}
		if v.TripID != nil {
			if d, ok := tripDelay[*v.TripID]; ok {
				vwd.Delay = &d
			}
		}
		result = append(result, vwd)
	}
	return result, vFeed.Timestamp, nil
}

// FetchVehiclesRaw returns the raw protobuf bytes for the vehicle positions feed.
// Used by the CORS proxy handler.
func (c *Client) FetchVehiclesRaw(ctx context.Context) ([]byte, error) {
	return c.fetchRaw(ctx, VehicleFeedPath)
}

func (c *Client) fetchFeed(ctx context.Context, path string) (*gtfsrt.FeedMessage, error) {
	body, err := c.fetchRaw(ctx, path)
	if err != nil {
		return nil, err
	}

	var feed gtfsrt.FeedMessage
	if err := pb.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode protobuf: %w", err)
	}

	if feed.Header == nil || feed.Header.Timestamp == nil {
		return nil, fmt.Errorf("feed missing header timestamp")
	}

	return &feed, nil
}

func (c *Client) fetchRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}

	return io.ReadAll(resp.Body)
}

func feedTimestamp(feed *gtfsrt.FeedMessage) time.Time {
	return time.Unix(int64(*feed.Header.Timestamp), 0).UTC()
}
