package api

// Stop is a transit stop with coordinates.
type Stop struct {
	StopID     string   `json:"stop_id"`
	StopName   string   `json:"stop_name"`
	Latitude   float64  `json:"lat"`
	Longitude  float64  `json:"lon"`
	Routes     int      `json:"routes,omitempty"`
	RouteIDs   []string `json:"route_ids,omitempty"`
	Transfer   bool     `json:"transfer,omitempty"`
	IsTerminal bool     `json:"is_terminal,omitempty"`
}

// StopPrediction is an upcoming arrival at a stop.
type StopPrediction struct {
	RouteID       string `json:"route_id"`
	RouteName     string `json:"route_name"`
	Headsign      string `json:"headsign"`
	ScheduledTime string `json:"scheduled"`
	PredictedTime string `json:"predicted"`
	DelaySec      *int   `json:"delay_seconds"`
	MinutesAway   int    `json:"minutes_away"`
	Status        string `json:"status"`
	RouteColor    string `json:"route_color"`
}

// PredictionsResponse wraps predictions with feed metadata.
type PredictionsResponse struct {
	Predictions []StopPrediction `json:"predictions"`
	UpdatedAt   *string          `json:"updated_at,omitempty"`
}

// StopAlert is alert info for a specific stop, passed to the map JS.
type StopAlert struct {
	Header      string `json:"header"`
	Description string `json:"description"`
}
