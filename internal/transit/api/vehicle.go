package api

// Vehicle is a bus position from the GTFS-RT vehicle feed.
type Vehicle struct {
	ID       string  `json:"id"`
	RouteID  string  `json:"route_id"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Bearing  float32 `json:"bearing"`
	Speed    float32 `json:"speed"`
	Status   string  `json:"status"`
	StopID   string  `json:"stop_id,omitempty"`
	NearStop string  `json:"near_stop,omitempty"`
	Delay    *int32  `json:"delay"`
}

// VehiclePayload is the SSE/JSON response for vehicle positions.
type VehiclePayload struct {
	Timestamp  int64            `json:"timestamp"`
	Vehicles   []Vehicle        `json:"vehicles"`
	StopServed map[string]int64 `json:"stop_served,omitempty"`
}
