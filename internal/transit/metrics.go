package transit

import "fmt"

// Band is one of the three time-of-day windows the application reports on.
// Hours outside 6am-12am are excluded — Thunder Bay Transit doesn't run
// before 6am, and there's nothing to measure there. The chunk subpackage
// at internal/transit/chunk treats bands as opaque strings; this struct
// adds the start/end hour bounds the SQL builder needs.
type Band struct {
	Name      string // "morning", "midday", "evening"
	StartHour int    // inclusive
	EndHour   int    // exclusive
}

// StartTime returns the band's lower bound as a "HH:MM:SS" string suitable
// for direct comparison against GTFS-style time columns.
func (b Band) StartTime() string { return fmt.Sprintf("%02d:00:00", b.StartHour) }

// EndTime is the exclusive upper bound, see StartTime.
func (b Band) EndTime() string { return fmt.Sprintf("%02d:00:00", b.EndHour) }

// Bands is the canonical list of time-of-day windows used everywhere in
// the application. Iterated by BuildChunksForDate to roll up one chunk
// per route per band per day.
var Bands = []Band{
	{Name: "morning", StartHour: 6, EndHour: 12},
	{Name: "midday", StartHour: 12, EndHour: 18},
	{Name: "evening", StartHour: 18, EndHour: 24},
}

// parseHMS converts a "HH:MM:SS" or "HH:MM" string into seconds-since-
// midnight. Used by repo.go and the trip planner. Returns 0 on parse
// failure (acceptable because callers all start at 0 and add).
func parseHMS(t string) int {
	var h, m, s int
	n, _ := fmt.Sscanf(t, "%d:%d:%d", &h, &m, &s)
	if n < 2 {
		return 0
	}
	return h*3600 + m*60 + s
}
