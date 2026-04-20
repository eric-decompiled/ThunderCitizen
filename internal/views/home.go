package views

import (
	"thundercitizen/internal/council"
	"thundercitizen/templates/components"
)

// NextMeeting is the editorial card shown on the home page for the next
// council session. Hand-curated — set NextCouncilMeeting below to nil to
// hide the card when there's nothing to highlight.
type NextMeeting struct {
	Date      string   // human-readable, e.g. "Tuesday, April 21"
	Time      string   // e.g. "6:30 PM"
	Type      string   // e.g. "City Council"
	AgendaURL string   // eSCRIBE FileStream link; empty if not posted yet
	EventURL  string   // calendar.thunderbay.ca event detail URL
	Summary   string   // 1–2 sentences on what's notable
	KeyItems  []string // optional agenda highlights
}

// HomeViewModel contains data for the home page
type HomeViewModel struct {
	Hero           components.HeroProps
	QuickLinks     []components.LinkedCardProps
	RecentMeetings []RecentMeetingView
	NextMeeting    *NextMeeting
}

// NextCouncilMeeting is the single source of truth for the home page card.
// Update when the next meeting is announced; set to nil to hide the card.
var NextCouncilMeeting = &NextMeeting{
	Date:      "Tuesday, April 21",
	Time:      "6:30 PM",
	Type:      "City Council",
	AgendaURL: "https://pub-thunderbay.escribemeetings.com/Meeting.aspx?Id=3c773247-1c29-4757-a367-0fe53fcce424&Agenda=Agenda&lang=English",
	EventURL:  "https://calendar.thunderbay.ca/default/Detail/2026-04-21-1830-City-Council",
	Summary:   "Zoning changes, a tourism tax update, and a proposed 2.7% council pay raise.",
	KeyItems: []string{
		"Rezoning at 116-222 Coady Ave and 1240 Dawson Rd",
		"Tourism & Municipal Accommodation Tax update",
		"2026 Council Remuneration — 2.7% increase proposed",
		"$68K in external funding for poverty reduction & food security",
	},
}

// RecentMeetingView is a compact meeting row for the home page.
type RecentMeetingView struct {
	Slug    string
	ID      string
	Date    string
	Summary string
	Motions int
}

// NewHomeViewModel creates the view model for the home page
func NewHomeViewModel(recentMeetings []council.MeetingSummary) HomeViewModel {
	recent := make([]RecentMeetingView, len(recentMeetings))
	for i, m := range recentMeetings {
		summary := m.Summary
		if len(summary) > 200 {
			cut := 200
			for cut > 150 && summary[cut] != ' ' {
				cut--
			}
			summary = summary[:cut] + "..."
		}
		recent[i] = RecentMeetingView{
			Slug:    council.MeetingSlug(m.Title, m.Date),
			ID:      m.ID,
			Date:    humanDate(m.Date),
			Summary: summary,
			Motions: m.MotionCount,
		}
	}

	return HomeViewModel{
		RecentMeetings: recent,
		NextMeeting:    NextCouncilMeeting,
		Hero: components.HeroProps{
			Title:    "Thunder Citizen",
			Lead:     "Data\u00a0for\u00a0the\u00a0People! (of\u00a0Thunder\u00a0Bay)",
			Subtitle: "",
		},
		QuickLinks: []components.LinkedCardProps{
			{
				Title:  "Budget",
				Href:   "/budget",
				Desc:   "Explore how your property taxes are allocated across city services.",
				Footer: "Budget visualizer",
			},
			{
				Title:  "Council",
				Href:   "/councillors",
				Desc:   "Browse voting records, key quotes, and decision-making patterns.",
				Footer: "Profiles · Voting records",
			},
			{
				Title:  "Transit",
				Href:   "/transit",
				Desc:   "Live bus tracking, service trends, and route finder.",
				Footer: "Live map · Metrics",
			},
		},
	}
}
