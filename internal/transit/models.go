package transit

import "time"

// AlertObservation is an append-only record of an alert seen at a feed timestamp.
type AlertObservation struct {
	FeedTimestamp  time.Time
	AlertID        string
	Cause          *string
	Effect         *string
	Header         *string
	Description    *string
	SeverityLevel  *string
	URL            *string
	ActiveStart    *time.Time
	ActiveEnd      *time.Time
	AffectedRoutes []string
	AffectedStops  []string
}

// TripCancellation records a CANCELED or DELETED trip from a trip update feed.
type TripCancellation struct {
	FeedTimestamp        time.Time
	TripID               string
	RouteID              string
	StartDate            *string
	StartTime            *string
	ScheduleRelationship string
}

// FeedGap records a detected gap in feed data.
type FeedGap struct {
	FeedType                string
	GapStart                time.Time
	GapEnd                  time.Time
	ExpectedIntervalSeconds int
	ActualGapSeconds        int
}

// TransitSnapshot is a system-wide aggregate for a 5-minute window.
type TransitSnapshot struct {
	CapturedAt       time.Time `json:"CapturedAt"`
	ActiveVehicles   int       `json:"ActiveVehicles"`
	ActiveRoutes     int       `json:"ActiveRoutes"`
	OnTimePct        float32   `json:"OnTimePct"`
	AvgDelaySeconds  float32   `json:"AvgDelaySeconds"`
	LateCount        int       `json:"LateCount"`
	EarlyCount       int       `json:"EarlyCount"`
	MeasurementCount int       `json:"MeasurementCount"`
	AlertCount       int       `json:"AlertCount"`
	Cancellations    int       `json:"Cancellations"`
}

// DaySummary is a daily aggregate of on-time performance.
type DaySummary struct {
	Date          time.Time `json:"date"`
	AvgOnTime     float32   `json:"avg_on_time"`
	AvgDelay      float32   `json:"avg_delay"`
	Cancellations int       `json:"cancellations"`
}

// ScheduledTrip is a trip from the GTFS schedule for a specific route and date.
type ScheduledTrip struct {
	TripID        string
	Headsign      string
	StartTime     string
	EndTime       string
	Canceled      bool
	AvgDelay      *float32 // average observed delay in seconds (nil if no data)
	StopsObserved int      // how many stops had actual data
	StopsTotal    int      // total stops on this trip
}
