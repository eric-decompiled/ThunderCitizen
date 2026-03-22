package views

import (
	"fmt"
	"strings"

	"thundercitizen/internal/council"
	"thundercitizen/internal/data"
	"thundercitizen/internal/models"
	"thundercitizen/templates/components"
)

// CouncillorVoteStatsView holds formatted voting statistics for display.
type CouncillorVoteStatsView struct {
	Attendance   string // "92%"
	ForCount     string // "45"
	AgainstCount string // "8"
	AbsentCount  string // "5"
	DissentRate  string // "12%"
	NotableVotes []NotableVoteView
}

// NotableVoteView is a presentation-ready notable vote.
type NotableVoteView struct {
	Summary  string
	Position string // "For" / "Against" / "Absent"
	Result   string // "Carried" / "Lost"
	Date     string
	URL      string // /minutes/{meetingID}
}

// CouncillorView is a view-ready councillor with presentation data
type CouncillorView struct {
	Name         string
	Position     string
	Term         string
	TermClass    string // CSS class for term badge color, e.g. "badge-term-1"
	Status       string
	Summary      string
	ShortSummary string
	ID           string
	Initials     string
	Photo        string                   // URL path e.g. "/static/councillors/boshcoff.jpg"
	VoteStats    *CouncillorVoteStatsView // nil for terms without DB data
}

// termBadgeClass returns a CSS class like "badge-term-3" from a term string like "3rd term".
func termBadgeClass(term string) string {
	for i, c := range term {
		if c < '0' || c > '9' {
			if i > 0 {
				return "badge-term-" + term[:i]
			}
			break
		}
	}
	return ""
}

// VoteMatrixColumn is a single motion (row in the flipped matrix).
type VoteMatrixColumn struct {
	Label      string // short label for the grid row
	FullTitle  string // full agenda item for modal
	Summary    string // LLM summary for modal
	Date       string
	Result     string // "Carried" / "Lost"
	URL        string // /minutes/{meetingID}#motion-{id}
	MeetingURL string // /minutes/{meetingID}
	IsKeyVote  bool   // headline significance
	MediaURL   string // press coverage link
}

// VoteMatrixRow is a single councillor row in the vote matrix.
type VoteMatrixRow struct {
	Name     string
	Initials string
	Photo    string   // URL path e.g. "/static/councillors/boshcoff.jpg"
	Cells    []string // "for", "against", or "" for each column
}

// VoteMatrixViewModel holds the councillor × motion grid.
type VoteMatrixViewModel struct {
	Columns []VoteMatrixColumn
	Rows    []VoteMatrixRow
}

// KeyVoteView is a presentation-ready key vote with optional media link.
type KeyVoteView struct {
	Issue    string
	Result   string
	Vote     string // "7-6" or ""
	MediaURL string // link to press coverage
	URL      string // link to motion in minutes
}

// TermVoteData holds all DB vote data for a single council term.
type TermVoteData struct {
	VoteStats     map[string]council.CouncillorVoteStats
	NotableVotes  map[string][]council.CouncillorNotableVote
	HeadlineVotes []council.HeadlineVote
	MatrixMotions []council.VoteMatrixMotion
	MatrixRecords []council.VoteMatrixRecord
}

// CouncillorsViewModel contains all data for the councillors page.
// Server-rendered for the selected term; HTMX swaps the content partial on term change.
type CouncillorsViewModel struct {
	TermSelector       components.YearSelectorProps
	CompensationStats  components.StatGridProps
	CompensationTitle  string
	Source             models.SourceRef
	Mayor              CouncillorView
	AtLargeCouncillors []CouncillorView
	WardCouncillors    []CouncillorView
	KeyVotes           []KeyVoteView
	KeyVotesTitle      string
	VoteMatrix         *VoteMatrixViewModel
}

// NewCouncillorsViewModel creates the view model for a single council term.
// The handler calls this for the selected term; HTMX swaps the content partial on change.
func NewCouncillorsViewModel(termYear int, vd TermVoteData) CouncillorsViewModel {
	t := data.CouncilByTerm[termYear]
	labels := data.ElectionLabels()
	label := labels[termYear]
	stats := t.Stats

	mayorView := councillorToView(t.Mayor, 0)
	mayorView.VoteStats = buildVoteStatsView(t.Mayor.Name, vd.VoteStats, vd.NotableVotes)

	atLargeViews := councillorsToViews(t.AtLarge)
	for i := range atLargeViews {
		atLargeViews[i].VoteStats = buildVoteStatsView(t.AtLarge[i].Name, vd.VoteStats, vd.NotableVotes)
	}

	wardViews := councillorsToViews(t.Ward)
	for i := range wardViews {
		wardViews[i].VoteStats = buildVoteStatsView(t.Ward[i].Name, vd.VoteStats, vd.NotableVotes)
	}

	vm := CouncillorsViewModel{
		TermSelector: components.YearSelectorProps{
			// 2018 term data was dropped pre-launch — only the current
			// term is supported until older terms are re-verified.
			Years:      []int{2022},
			Current:    termYear,
			Labels:     labels,
			AriaLabel:  "Select election term",
			BaseURL:    "/councillors",
			ParamName:  "term",
			HTMXTarget: "#councillor-content",
		},
		CompensationStats: components.StatGridProps{
			Columns: 3,
			Items: []components.StatItem{
				{Label: "Total Annual", Value: stats.TotalAnnual, Note: stats.SalaryIncreaseNote},
				{Label: "Mayor", Value: stats.MayorSalary, Note: "plus expenses, benefits"},
				{Label: "Councillors (12)", Value: stats.CouncillorSalary, Note: "plus expenses, benefits"},
			},
		},
		CompensationTitle:  "Council Compensation (" + label + ")",
		Source:             stats.Source,
		Mayor:              mayorView,
		AtLargeCouncillors: atLargeViews,
		WardCouncillors:    wardViews,
		KeyVotes:           buildKeyVotes(t.KeyVotes, vd.HeadlineVotes),
		KeyVotesTitle:      "Key Votes (" + label + ")",
	}
	vm.VoteMatrix = BuildVoteMatrix(vd.MatrixMotions, vd.MatrixRecords, vd.HeadlineVotes)
	return vm
}

// lastName extracts the last word from a full name for matching against vote records.
func lastName(fullName string) string {
	parts := strings.Fields(fullName)
	if len(parts) == 0 {
		return fullName
	}
	return parts[len(parts)-1]
}

// findVoteStats looks up vote stats by trying the full name first, then last name.
func findVoteStats(name string, stats map[string]council.CouncillorVoteStats) (council.CouncillorVoteStats, bool) {
	if stats == nil {
		return council.CouncillorVoteStats{}, false
	}
	if s, ok := stats[name]; ok {
		return s, true
	}
	last := lastName(name)
	for k, s := range stats {
		if lastName(k) == last {
			return s, true
		}
	}
	return council.CouncillorVoteStats{}, false
}

// findNotableVotes looks up notable votes by trying the full name first, then last name.
func findNotableVotes(name string, nvs map[string][]council.CouncillorNotableVote) []council.CouncillorNotableVote {
	if nvs == nil {
		return nil
	}
	if v, ok := nvs[name]; ok {
		return v
	}
	last := lastName(name)
	for k, v := range nvs {
		if lastName(k) == last {
			return v
		}
	}
	return nil
}

func formatPercent(num, denom int) string {
	if denom == 0 {
		return "—"
	}
	pct := float64(num) * 100.0 / float64(denom)
	return fmt.Sprintf("%.0f%%", pct)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func resultDisplay(r string) string {
	switch r {
	case "CARRIED":
		return "Carried"
	case "LOST":
		return "Lost"
	case "TIE":
		return "Tie"
	default:
		return r
	}
}

func buildVoteStatsView(
	name string,
	stats map[string]council.CouncillorVoteStats,
	nvs map[string][]council.CouncillorNotableVote,
) *CouncillorVoteStatsView {
	cs, ok := findVoteStats(name, stats)
	if !ok {
		return nil
	}

	view := &CouncillorVoteStatsView{
		Attendance:   formatPercent(cs.VotesCast(), cs.TotalRecorded()),
		ForCount:     itoa(cs.ForCount),
		AgainstCount: itoa(cs.AgainstCount),
		AbsentCount:  itoa(cs.AbsentCount),
		DissentRate:  formatPercent(cs.DissentCount, cs.VotesCast()),
	}

	for _, nv := range findNotableVotes(name, nvs) {
		view.NotableVotes = append(view.NotableVotes, NotableVoteView{
			Summary:  nv.Summary,
			Position: titleCase(nv.Position),
			Result:   resultDisplay(nv.Result),
			Date:     nv.Date,
			URL:      "/minutes/" + nv.MeetingID,
		})
	}

	return view
}

func councillorToView(c models.Councillor, index int) CouncillorView {
	var photo string
	if c.Photo != "" {
		photo = "/static/councillors/" + c.Photo
	}
	return CouncillorView{
		Name:         c.Name,
		Position:     c.Position,
		Term:         c.Term,
		TermClass:    termBadgeClass(c.Term),
		Status:       c.Status,
		Summary:      c.Summary,
		ShortSummary: c.ShortSummary,
		ID:           CouncillorID(string(c.Type), index),
		Initials:     Initials(c.Name),
		Photo:        photo,
	}
}

func councillorsToViews(councillors []models.Councillor) []CouncillorView {
	views := make([]CouncillorView, len(councillors))
	for i, c := range councillors {
		views[i] = councillorToView(c, i)
	}
	return views
}

// BuildVoteMatrix constructs the view model for the councillor × motion grid.
func BuildVoteMatrix(
	motions []council.VoteMatrixMotion,
	records []council.VoteMatrixRecord,
	headlines []council.HeadlineVote,
) *VoteMatrixViewModel {
	if len(motions) == 0 {
		return nil
	}

	// Build headline media URL lookup by motion ID
	headlineMedia := make(map[int64]string)
	for _, hv := range headlines {
		if hv.MediaURL != "" {
			headlineMedia[hv.MotionID] = hv.MediaURL
		}
	}

	// Build columns
	columns := make([]VoteMatrixColumn, len(motions))
	motionIndex := make(map[int64]int) // motion ID → column index
	for i, m := range motions {
		mediaURL := m.MediaURL
		if mediaURL == "" {
			mediaURL = headlineMedia[m.ID]
		}
		columns[i] = VoteMatrixColumn{
			Label:      m.AgendaItem,
			FullTitle:  m.FullTitle,
			Summary:    m.Summary,
			Date:       humanDate(m.Date),
			Result:     resultDisplay(m.Result),
			URL:        fmt.Sprintf("/minutes/%s#motion-%d", m.MeetingID, m.ID),
			MeetingURL: fmt.Sprintf("/minutes/%s", m.MeetingID),
			IsKeyVote:  m.Significance == "headline",
			MediaURL:   mediaURL,
		}
		motionIndex[m.ID] = i
	}

	// Group records by councillor
	byCouncillor := make(map[string][]council.VoteMatrixRecord)
	for _, r := range records {
		byCouncillor[r.Councillor] = append(byCouncillor[r.Councillor], r)
	}

	// Build councillor order and photo lookup from council data (mayor → at-large → ward)
	photoByName := make(map[string]string)
	var councillorOrder []string
	seen := make(map[string]bool)
	for _, term := range data.CouncilByTerm {
		all := append(append([]models.Councillor{term.Mayor}, term.AtLarge...), term.Ward...)
		for _, c := range all {
			if c.Photo != "" {
				photoByName[c.Name] = "/static/councillors/" + c.Photo
			}
			// Add to order if they have vote records and aren't already listed
			if !seen[c.Name] && len(byCouncillor[c.Name]) > 0 {
				seen[c.Name] = true
				councillorOrder = append(councillorOrder, c.Name)
			}
		}
	}
	// Append any councillors from vote records not in static data
	for name := range byCouncillor {
		if !seen[name] {
			seen[name] = true
			councillorOrder = append(councillorOrder, name)
		}
	}

	// Build rows
	rows := make([]VoteMatrixRow, len(councillorOrder))
	for i, name := range councillorOrder {
		cells := make([]string, len(motions))
		for _, r := range byCouncillor[name] {
			if idx, ok := motionIndex[r.MotionID]; ok {
				cells[idx] = r.Position
			}
		}
		rows[i] = VoteMatrixRow{
			Name:     name,
			Initials: Initials(name),
			Photo:    photoByName[name],
			Cells:    cells,
		}
	}

	return &VoteMatrixViewModel{
		Columns: columns,
		Rows:    rows,
	}
}

// buildKeyVotes returns DB headline votes if available, falling back to static data.
func buildKeyVotes(static []models.KeyVote, headlines []council.HeadlineVote) []KeyVoteView {
	if len(headlines) > 0 {
		views := make([]KeyVoteView, len(headlines))
		for i, hv := range headlines {
			views[i] = KeyVoteView{
				Issue:    hv.AgendaItem,
				Result:   resultDisplay(hv.Result),
				Vote:     hv.VoteTally,
				MediaURL: hv.MediaURL,
				URL:      fmt.Sprintf("/minutes/%s#motion-%d", hv.MeetingID, hv.MotionID),
			}
		}
		return views
	}
	views := make([]KeyVoteView, len(static))
	for i, kv := range static {
		views[i] = KeyVoteView{
			Issue:    kv.Issue,
			Result:   kv.Result,
			Vote:     kv.Vote,
			MediaURL: kv.MediaURL,
		}
	}
	return views
}

// CouncillorPageViewModel is the view model for /councillors/{slug}.
type CouncillorPageViewModel struct {
	Councillor CouncillorView
	VoteStats  *CouncillorVoteStatsView
	VoteRecord []CouncillorVoteRecordRow
	Term       string
	TermLabel  string
}

// CouncillorVoteRecordRow is a single vote in the councillor's voting record.
type CouncillorVoteRecordRow struct {
	Date        string
	AgendaItem  string
	Summary     string
	Position    string // "For", "Against", "Absent"
	PositionCSS string // "for", "against", "absent"
	Result      string // "Carried", "Lost"
	ResultCSS   string // "carried", "lost"
	VoteTally   string // "7-6"
	URL         string // /minutes/{meetingID}#motion-{id}
}

// NewCouncillorPageViewModel builds the view model for a councillor's page.
func NewCouncillorPageViewModel(
	c models.Councillor,
	termYear int,
	stats map[string]council.CouncillorVoteStats,
	notableVotes map[string][]council.CouncillorNotableVote,
	voteRecord []council.CouncillorVoteRow,
) CouncillorPageViewModel {
	cv := councillorToView(c, 0)
	cv.VoteStats = buildVoteStatsView(c.Name, stats, notableVotes)

	rows := make([]CouncillorVoteRecordRow, len(voteRecord))
	for i, v := range voteRecord {
		rows[i] = CouncillorVoteRecordRow{
			Date:        v.Date,
			AgendaItem:  v.AgendaItem,
			Summary:     v.Summary,
			Position:    titleCase(v.Position),
			PositionCSS: v.Position,
			Result:      resultDisplay(v.Result),
			ResultCSS:   strings.ToLower(resultDisplay(v.Result)),
			URL:         fmt.Sprintf("/minutes/%s#motion-%d", v.MeetingID, v.MotionID),
		}
		if v.YeaCount+v.NayCount > 0 {
			rows[i].VoteTally = fmt.Sprintf("%d–%d", v.YeaCount, v.NayCount)
		}
	}

	termLabel := ""
	for _, y := range data.AvailableTerms() {
		if y == termYear {
			termLabel = data.ElectionLabel(y)
			break
		}
	}

	return CouncillorPageViewModel{
		Councillor: cv,
		VoteStats:  cv.VoteStats,
		VoteRecord: rows,
		Term:       data.TermRange(termYear),
		TermLabel:  termLabel,
	}
}
