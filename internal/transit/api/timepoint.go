package api

// Timepoint is a schedule-adherence stop with route associations.
type Timepoint struct {
	StopID string   `json:"stop_id"`
	Routes []string `json:"routes"`
	Colors []string `json:"colors"`
}
