package council

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/fetch"
)

const (
	// PDFDir is where individual minutes PDFs are downloaded.
	PDFDir = "static/councillors/minutes"
	// VotesOutDir is where the per-term votes_*.json files land.
	VotesOutDir = "static/councillors"
	// SummariesPath is the optional flat file of LLM-generated summaries.
	SummariesPath = "static/councillors/summaries.json"
)

// VotesFetchOptions controls which meetings the eSCRIBE scraper targets.
type VotesFetchOptions struct {
	// Term, if non-zero, restricts the fetch to one council term (2018 or 2022).
	Term int
	// SkipDownload re-parses local PDFs without hitting the network for downloads.
	// Discovery (the meeting list call) still happens.
	SkipDownload bool
}

// DiscoverVoteSources lists every meeting PDF that the votes scraper would
// download, after applying the term filter. The eSCRIBE meeting list endpoint
// IS called (it's the cheap discovery step), but no PDFs are downloaded.
func DiscoverVoteSources(_ context.Context, opts VotesFetchOptions) ([]fetch.Source, error) {
	meetings, err := ListMeetings()
	if err != nil {
		return nil, fmt.Errorf("listing meetings: %w", err)
	}

	if opts.Term != 0 {
		termLabel := fmt.Sprintf("%d-%d", opts.Term, opts.Term+4)
		meetings = filterMeetingsByTerm(meetings, termLabel)
	}

	out := make([]fetch.Source, 0, len(meetings))
	for _, m := range meetings {
		if m.MinutesURL == "" {
			continue
		}
		out = append(out, fetch.Source{
			Label: fmt.Sprintf("%s  %s", m.Date, m.PDFFile),
			URL:   m.MinutesURL,
		})
	}
	return out, nil
}

// FetchVotes is the canonical scrape-and-persist routine for council votes.
// Steps: list meetings via eSCRIBE → optionally download PDFs → parse text →
// extract motions/votes → save to DB (if pool != nil) → write votes_*.json
// per term. Auto-loads summaries.json if present and pool != nil.
func FetchVotes(ctx context.Context, opts VotesFetchOptions, pool *pgxpool.Pool) error {
	if err := CheckPdftotext(); err != nil {
		return err
	}
	if err := os.MkdirAll(PDFDir, 0o755); err != nil {
		return fmt.Errorf("creating pdf dir: %w", err)
	}

	fmt.Println("Fetching meeting list from eSCRIBE...")
	meetings, err := ListMeetings()
	if err != nil {
		return fmt.Errorf("listing meetings: %w", err)
	}
	fmt.Printf("  Found %d meetings with minutes\n", len(meetings))

	if opts.Term != 0 {
		termLabel := fmt.Sprintf("%d-%d", opts.Term, opts.Term+4)
		meetings = filterMeetingsByTerm(meetings, termLabel)
		fmt.Printf("  Filtered to %d meetings for term %s\n", len(meetings), termLabel)
	}

	if !opts.SkipDownload {
		downloaded := 0
		for i, m := range meetings {
			if m.MinutesURL == "" || m.PDFFile == "" {
				continue
			}
			_, err := DownloadPDF(m.MinutesURL, PDFDir, m.PDFFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
				continue
			}
			downloaded++
			if (i+1)%10 == 0 {
				fmt.Printf("  Progress: %d/%d meetings\n", i+1, len(meetings))
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Printf("  Downloaded/verified %d PDFs\n", downloaded)
	}

	fmt.Println("Parsing minutes PDFs...")
	totalMotions, totalRecordedVotes := 0, 0
	for i := range meetings {
		m := &meetings[i]
		pdfPath := filepath.Join(PDFDir, m.PDFFile)
		if _, err := os.Stat(pdfPath); err != nil {
			continue
		}
		text, err := ExtractText(pdfPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %s: %v\n", m.PDFFile, err)
			continue
		}
		m.Motions = ParseMotions(text)
		totalMotions += len(m.Motions)
		for _, mot := range m.Motions {
			if mot.Votes != nil {
				totalRecordedVotes++
			}
		}
	}
	fmt.Printf("  Parsed %d motions (%d with recorded votes)\n", totalMotions, totalRecordedVotes)

	if pool != nil {
		store := NewStore(pool)
		if err := store.SaveMeetings(ctx, meetings); err != nil {
			return fmt.Errorf("saving to database: %w", err)
		}
		fmt.Printf("  Saved %d meetings to database\n", len(meetings))

		if _, err := os.Stat(SummariesPath); err == nil {
			fmt.Println("  Loading LLM summaries from summaries.json...")
			if err := loadSummariesFromFile(ctx, pool); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: loading summaries: %v\n", err)
			}
		}
	}

	return writeVotesJSON(meetings)
}

// ExportVotes pulls meetings from the DB and re-emits the per-term JSON files.
// Used by the operator after a re-summarize / manual edit pass.
func ExportVotes(ctx context.Context, opts VotesFetchOptions, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("export requires a database connection")
	}
	store := NewStore(pool)

	terms := []string{"2022-2026"}
	if opts.Term != 0 {
		terms = []string{fmt.Sprintf("%d-%d", opts.Term, opts.Term+4)}
	}

	for _, termLabel := range terms {
		meetings, err := store.LoadMeetings(ctx, termLabel)
		if err != nil {
			return fmt.Errorf("loading %s: %w", termLabel, err)
		}
		if len(meetings) == 0 {
			fmt.Printf("  No meetings for %s, skipping\n", termLabel)
			continue
		}
		sort.Slice(meetings, func(i, j int) bool { return meetings[i].Date < meetings[j].Date })

		idx := MeetingIndex{
			Term:      termLabel,
			ScrapedAt: time.Now().UTC(),
			Meetings:  meetings,
		}
		electionYear := termLabel[:4]
		outPath := filepath.Join(VotesOutDir, fmt.Sprintf("votes_%s.json", electionYear))
		out, err := json.MarshalIndent(idx, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling %s: %w", termLabel, err)
		}
		if err := os.WriteFile(outPath, out, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", outPath, err)
		}

		motionCount, enriched := 0, 0
		for _, m := range meetings {
			motionCount += len(m.Motions)
			for _, mot := range m.Motions {
				if mot.Summary != "" {
					enriched++
				}
			}
		}
		fmt.Printf("  Exported %s (%d meetings, %d motions, %d with summaries)\n",
			outPath, len(meetings), motionCount, enriched)
	}
	return nil
}

func filterMeetingsByTerm(meetings []Meeting, termLabel string) []Meeting {
	var out []Meeting
	for _, m := range meetings {
		if TermForDate(m.Date) == termLabel {
			out = append(out, m)
		}
	}
	return out
}

func writeVotesJSON(meetings []Meeting) error {
	byTerm := map[string][]Meeting{}
	for _, m := range meetings {
		t := TermForDate(m.Date)
		byTerm[t] = append(byTerm[t], m)
	}
	for termLabel, termMeetings := range byTerm {
		sort.Slice(termMeetings, func(i, j int) bool {
			return termMeetings[i].Date < termMeetings[j].Date
		})

		idx := MeetingIndex{
			Term:      termLabel,
			ScrapedAt: time.Now().UTC(),
			Meetings:  termMeetings,
		}
		electionYear := termLabel[:4]
		outPath := filepath.Join(VotesOutDir, fmt.Sprintf("votes_%s.json", electionYear))
		out, err := json.MarshalIndent(idx, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling %s: %w", termLabel, err)
		}
		if err := os.WriteFile(outPath, out, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", outPath, err)
		}
		motionCount, voteCount := 0, 0
		for _, m := range termMeetings {
			motionCount += len(m.Motions)
			for _, mot := range m.Motions {
				if mot.Votes != nil {
					voteCount++
				}
			}
		}
		fmt.Printf("  Wrote %s (%d meetings, %d motions, %d recorded votes)\n",
			outPath, len(termMeetings), motionCount, voteCount)
	}
	return nil
}

// loadSummariesFromFile applies LLM-generated motion/meeting summaries
// from the flat summaries.json file to rows that don't already have one.
// This is an UPDATE pass over council_motions and council_meetings —
// matched by (meeting_id, agenda_item) for motions and (id) for meetings.
func loadSummariesFromFile(ctx context.Context, pool *pgxpool.Pool) error {
	data, err := os.ReadFile(SummariesPath)
	if err != nil {
		return err
	}
	var export struct {
		Motions []struct {
			MeetingID       string `json:"meeting_id"`
			AgendaItem      string `json:"agenda_item"`
			LLMSummary      string `json:"llm_summary"`
			LLMLabel        string `json:"llm_label"`
			LLMSignificance string `json:"llm_significance"`
			LLMModel        string `json:"llm_model"`
			Significance    string `json:"significance"`
			MediaURL        string `json:"media_url"`
		} `json:"motions"`
		Meetings []struct {
			MeetingID  string `json:"meeting_id"`
			LLMSummary string `json:"llm_summary"`
			LLMModel   string `json:"llm_model"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(data, &export); err != nil {
		return err
	}

	motionUpdated := 0
	for _, r := range export.Motions {
		tag, err := pool.Exec(ctx, `
			UPDATE council_motions
			SET llm_summary = $1, llm_label = $2, llm_significance = $3, llm_model = $4,
			    significance = $5, media_url = NULLIF($6, '')
			WHERE meeting_id = $7 AND agenda_item = $8 AND llm_summary = ''`,
			r.LLMSummary, r.LLMLabel, r.LLMSignificance, r.LLMModel,
			r.Significance, r.MediaURL,
			r.MeetingID, r.AgendaItem)
		if err != nil {
			continue
		}
		motionUpdated += int(tag.RowsAffected())
	}

	meetingUpdated := 0
	for _, r := range export.Meetings {
		tag, err := pool.Exec(ctx, `
			UPDATE council_meetings
			SET llm_summary = $1, llm_model = $2
			WHERE id = $3 AND llm_summary = ''`,
			r.LLMSummary, r.LLMModel, r.MeetingID)
		if err != nil {
			continue
		}
		meetingUpdated += int(tag.RowsAffected())
	}
	fmt.Printf("  Loaded %d motion summaries, %d meeting summaries\n", motionUpdated, meetingUpdated)
	return nil
}
