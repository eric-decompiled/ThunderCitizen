package api

// PlanResponse is the trip planner result.
type PlanResponse struct {
	Itineraries []Itinerary `json:"itineraries"`
}

// Itinerary is a single trip option.
type Itinerary struct {
	Departure         string   `json:"departure"`
	Arrival           string   `json:"arrival"`
	DurationMin       int      `json:"duration_min"`
	Transfers         int      `json:"transfers"`
	LeaveBy           string   `json:"leave_by,omitempty"`
	Label             string   `json:"label,omitempty"`
	NextDepartures    []string `json:"next_departures,omitempty"`
	Legs              []Leg    `json:"legs"`
	Cancelled         bool     `json:"cancelled,omitempty"`
	CancelledSavedMin int      `json:"cancelled_saved_min,omitempty"`
}

// Leg is one segment of a trip (walk or transit).
type Leg struct {
	Type        string  `json:"type"`
	RouteID     string  `json:"route_id,omitempty"`
	RouteName   string  `json:"route_name,omitempty"`
	RouteColor  string  `json:"route_color,omitempty"`
	Headsign    string  `json:"headsign,omitempty"`
	From        LegStop `json:"from"`
	To          LegStop `json:"to"`
	Departure   string  `json:"departure,omitempty"`
	Arrival     string  `json:"arrival,omitempty"`
	DurationMin int     `json:"duration_min"`
	DistanceM   float64 `json:"distance_m,omitempty"`
	NumStops    int     `json:"stops,omitempty"`
	Hint        string  `json:"hint,omitempty"`
}

// LegStop is an endpoint of a trip leg.
type LegStop struct {
	StopID      string  `json:"stop_id,omitempty"`
	Name        string  `json:"name"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	DestDistM   float64 `json:"dest_distance_m,omitempty"`
	DestWalkMin int     `json:"dest_walk_min,omitempty"`
}
