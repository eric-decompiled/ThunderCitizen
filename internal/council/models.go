package council

import "time"

// MeetingIndex is the top-level output for a council term.
type MeetingIndex struct {
	Term      string    `json:"term"`
	ScrapedAt time.Time `json:"scraped_at"`
	Meetings  []Meeting `json:"meetings"`
}

// Meeting represents a single council meeting with extracted votes.
type Meeting struct {
	ID         string   `json:"id"`
	Date       string   `json:"date"`
	Title      string   `json:"title"`
	MinutesURL string   `json:"minutes_url"`
	PDFFile    string   `json:"pdf_file"`
	Summary    string   `json:"summary,omitempty"` // LLM meeting summary
	Motions    []Motion `json:"motions"`
}

// Motion represents a council motion (MOVED BY block) with optional recorded vote.
type Motion struct {
	AgendaItem   string      `json:"agenda_item,omitempty"` // heading before the motion (e.g. "Report Back - Temporary Shelter Village")
	MovedBy      string      `json:"moved_by"`
	SecondedBy   string      `json:"seconded_by"`
	Text         string      `json:"text"`
	Result       string      `json:"result"`
	Significance string      `json:"significance,omitempty"` // headline, notable, routine, procedural
	Summary      string      `json:"summary,omitempty"`      // LLM plain-language summary
	Label        string      `json:"label,omitempty"`        // LLM short label (~60 chars)
	MediaURL     string      `json:"media_url,omitempty"`    // press coverage URL (headline votes)
	RawText      string      `json:"raw_text,omitempty"`     // raw PDF text for audit (recorded votes only)
	Votes        *VoteRecord `json:"votes,omitempty"`

	// Internal — used by parser to map recorded votes to motions.
	blockStart int
	blockEnd   int
}

// VoteRecord holds per-councillor vote breakdown.
type VoteRecord struct {
	For     []string `json:"for"`
	Against []string `json:"against"`
	Absent  []string `json:"absent,omitempty"`
}
