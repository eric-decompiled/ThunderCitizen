package api

// RouteShape is a route polyline for the map.
type RouteShape struct {
	RouteID     string       `json:"route_id"`
	Coordinates [][2]float64 `json:"coordinates"`
}
