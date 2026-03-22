package views

import (
	"thundercitizen/internal/council"
	"thundercitizen/templates/components"
)

// UpcomingDate is a date-labelled item for the home page.
type UpcomingDate struct {
	Date string
	Desc string
	Link string // source URL
}

// HomeViewModel contains data for the home page
type HomeViewModel struct {
	Hero           components.HeroProps
	QuickLinks     []components.LinkedCardProps
	RecentMeetings []RecentMeetingView
	Upcoming       []UpcomingDate
}

// RecentMeetingView is a compact meeting row for the home page.
type RecentMeetingView struct {
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
			ID:      m.ID,
			Date:    humanDate(m.Date),
			Summary: summary,
			Motions: m.MotionCount,
		}
	}

	return HomeViewModel{
		RecentMeetings: recent,
		Upcoming: []UpcomingDate{
			{Date: "April 7", Desc: "City Council", Link: "https://calendar.thunderbay.ca/default/Detail/2026-04-07-1830-City-Council"},
			{Date: "April 21", Desc: "City Council", Link: "https://calendar.thunderbay.ca/default/Detail/2026-04-21-1830-City-Council"},
			{Date: "April 28", Desc: "Standing Committee — Quality of Life", Link: "https://calendar.thunderbay.ca/default/Detail/2026-04-28-1630-Standing-Committee-Quality-of-Life"},
		},
		Hero: components.HeroProps{
			Title:    "Thunder Citizen",
			Lead:     "Data for the People! (of Thunder Bay)",
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
