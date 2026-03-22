package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/config"
)

const summaryFile = "static/councillors/summaries.json"

// SummaryRecord holds the LLM-generated data for one motion, keyed by meeting+agenda.
type SummaryRecord struct {
	MeetingID       string `json:"meeting_id"`
	MeetingDate     string `json:"meeting_date"`
	AgendaItem      string `json:"agenda_item"`
	LLMSummary      string `json:"llm_summary"`
	LLMLabel        string `json:"llm_label"`
	LLMSignificance string `json:"llm_significance"`
	LLMModel        string `json:"llm_model"`
	Significance    string `json:"significance"` // manual override
	MediaURL        string `json:"media_url"`
}

// MeetingSummaryRecord holds the LLM summary for a meeting.
type MeetingSummaryRecord struct {
	MeetingID  string `json:"meeting_id"`
	Date       string `json:"date"`
	LLMSummary string `json:"llm_summary"`
	LLMModel   string `json:"llm_model"`
}

// SummaryExport is the top-level structure of the summaries flat file.
type SummaryExport struct {
	ExportedAt string                 `json:"exported_at"`
	Motions    []SummaryRecord        `json:"motions"`
	Meetings   []MeetingSummaryRecord `json:"meetings"`
}

func exportSummaries() error {
	dbURL := config.Secret("DATABASE_URL", "postgres://localhost:5432/thundercitizen?sslmode=disable")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	// Export motion summaries
	rows, err := pool.Query(ctx, `
		SELECT mo.meeting_id, m.date::text, mo.agenda_item,
		       COALESCE(mo.llm_summary, ''), COALESCE(mo.llm_label, ''),
		       COALESCE(mo.llm_significance, ''), COALESCE(mo.llm_model, ''),
		       mo.significance, COALESCE(mo.media_url, '')
		FROM council_motions mo
		JOIN council_meetings m ON m.id = mo.meeting_id
		WHERE mo.llm_summary != '' OR mo.significance != 'routine' OR mo.media_url IS NOT NULL
		ORDER BY m.date, mo.agenda_item`)
	if err != nil {
		return fmt.Errorf("querying motions: %w", err)
	}
	defer rows.Close()

	var motions []SummaryRecord
	for rows.Next() {
		var r SummaryRecord
		if err := rows.Scan(&r.MeetingID, &r.MeetingDate, &r.AgendaItem,
			&r.LLMSummary, &r.LLMLabel, &r.LLMSignificance, &r.LLMModel,
			&r.Significance, &r.MediaURL); err != nil {
			return fmt.Errorf("scanning motion: %w", err)
		}
		motions = append(motions, r)
	}

	// Export meeting summaries
	mRows, err := pool.Query(ctx, `
		SELECT id, date::text, COALESCE(llm_summary, ''), COALESCE(llm_model, '')
		FROM council_meetings
		WHERE llm_summary != ''
		ORDER BY date`)
	if err != nil {
		return fmt.Errorf("querying meetings: %w", err)
	}
	defer mRows.Close()

	var meetings []MeetingSummaryRecord
	for mRows.Next() {
		var r MeetingSummaryRecord
		if err := mRows.Scan(&r.MeetingID, &r.Date, &r.LLMSummary, &r.LLMModel); err != nil {
			return fmt.Errorf("scanning meeting: %w", err)
		}
		meetings = append(meetings, r)
	}

	export := SummaryExport{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Motions:    motions,
		Meetings:   meetings,
	}

	out, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}

	if err := os.WriteFile(summaryFile, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", summaryFile, err)
	}

	fmt.Printf("Exported %d motion summaries, %d meeting summaries to %s\n",
		len(motions), len(meetings), summaryFile)
	return nil
}

func importSummaries() error {
	dbURL := config.Secret("DATABASE_URL", "postgres://localhost:5432/thundercitizen?sslmode=disable")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	data, err := os.ReadFile(summaryFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", summaryFile, err)
	}

	var export SummaryExport
	if err := json.Unmarshal(data, &export); err != nil {
		return fmt.Errorf("parsing %s: %w", summaryFile, err)
	}

	// Import motion summaries — match by meeting_id + agenda_item
	motionUpdated := 0
	for _, r := range export.Motions {
		tag, err := pool.Exec(ctx, `
			UPDATE council_motions
			SET llm_summary = $1, llm_label = $2, llm_significance = $3, llm_model = $4,
			    significance = $5, media_url = NULLIF($6, '')
			WHERE meeting_id = $7 AND agenda_item = $8
			  AND llm_summary = ''`,
			r.LLMSummary, r.LLMLabel, r.LLMSignificance, r.LLMModel,
			r.Significance, r.MediaURL,
			r.MeetingID, r.AgendaItem)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: motion %s/%s: %v\n", r.MeetingID, r.AgendaItem, err)
			continue
		}
		motionUpdated += int(tag.RowsAffected())
	}

	// Import meeting summaries
	meetingUpdated := 0
	for _, r := range export.Meetings {
		tag, err := pool.Exec(ctx, `
			UPDATE council_meetings
			SET llm_summary = $1, llm_model = $2
			WHERE id = $3 AND llm_summary = ''`,
			r.LLMSummary, r.LLMModel, r.MeetingID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: meeting %s: %v\n", r.MeetingID, err)
			continue
		}
		meetingUpdated += int(tag.RowsAffected())
	}

	fmt.Printf("Imported %d/%d motion summaries, %d/%d meeting summaries from %s\n",
		motionUpdated, len(export.Motions), meetingUpdated, len(export.Meetings), summaryFile)
	return nil
}
