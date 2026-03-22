package transit

import "time"

// Data collection intervals — how often we record from live feeds.
// These are surfaced on the About page so they stay in sync with the code.
const (
	VehiclePollInterval = 6 * time.Second
	TripPollInterval    = 60 * time.Second
	AlertPollInterval   = 60 * time.Second
	GTFSRefreshInterval = 4 * time.Hour
)
