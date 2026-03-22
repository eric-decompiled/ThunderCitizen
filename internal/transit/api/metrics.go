package api

// CancelledTrip is a cancelled trip record. Kept here for the cancel-log
// surface (separate concern from chunks).
type CancelledTrip struct {
	TripID    string `json:"trip_id"`
	RouteID   string `json:"route_id"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Headsign  string `json:"headsign"`
	Upcoming  bool   `json:"upcoming"`
	LeadMin   int    `json:"lead_min"`
	LeadLabel string `json:"lead_label"`
}
