package muni

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plugins are registered in dependency order. Extract and Apply iterate
// in this order — register parents before children.
func init() {
	Register(&CouncillorsPlugin{})
	Register(&CouncilMeetingsPlugin{})
	Register(&CouncilMotionsPlugin{})
	Register(&CouncilVotesPlugin{})
	Register(&BudgetAccountsPlugin{})
	Register(&BudgetLedgerPlugin{})
}

const collectedNow = "" // sentinel — plugins call time.Now() in Extract

// ─── Councillors ────────────────────────────────────────────────────────

type CouncillorsPlugin struct{}

func (CouncillorsPlugin) Name() string { return "councillors" }

func (CouncillorsPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "councillors.tsv"
	cols := []string{"name", "council_type", "term", "position", "term_number", "status", "summary", "short_summary", "photo", "source"}
	query := `SELECT name, council_type, term, "position", term_number, status,
	          summary, short_summary, photo, source
	          FROM councillors ORDER BY term, council_type, name`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}
	return []Dataset{{
		File: file, Plugin: "councillors", Table: "councillors",
		SourceURL:   "https://thunderbay.ca/en/city-hall/mayor-and-council-profiles.aspx",
		SourceDoc:   "https://thunderbay.ca/en/city-hall/mayor-and-council-profiles.aspx",
		Description: "Councillor biographies 2010-2026",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "hand-curated",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (CouncillorsPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	header, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging")
	if err != nil {
		return 0, err
	}
	return ds.Rows, SimpleUpsert(ctx, tx, ds.Table, "muni_staging", header)
}

// ─── Council meetings ───────────────────────────────────────────────────

type CouncilMeetingsPlugin struct{}

func (CouncilMeetingsPlugin) Name() string { return "council_meetings" }

func (CouncilMeetingsPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "council_meetings.tsv"
	cols := []string{"id", "date", "title", "term", "minutes_url", "pdf_file", "llm_summary", "llm_model", "source"}
	query := `SELECT id, date::text, title, term, minutes_url, pdf_file,
	          llm_summary, llm_model, source
	          FROM council_meetings WHERE term = '2022-2026' ORDER BY date`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}

	// Sidecar provenance file — per-document source URLs.
	sourcesFile := "council_meetings.sources.tsv"
	sourcesCols := []string{"meeting_id", "date", "url", "pdf_file", "scraped_at"}
	sourcesQuery := `SELECT id, date::text, minutes_url, pdf_file, scraped_at::text
	                 FROM council_meetings WHERE term = '2022-2026' ORDER BY date`
	if _, _, err := runExtractQuery(ctx, pool, outDir, sourcesFile, sourcesQuery, sourcesCols); err != nil {
		return nil, err
	}

	return []Dataset{{
		File: file, Plugin: "council_meetings", Table: "council_meetings",
		SourceURL:   "https://pub-thunderbay.escribemeetings.com",
		SourceDoc:   sourcesFile,
		Description: "Council meetings 2022-2026",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "scraped+llm",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (CouncilMeetingsPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	header, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging")
	if err != nil {
		return 0, err
	}
	return ds.Rows, SimpleUpsert(ctx, tx, ds.Table, "muni_staging", header)
}

// ─── Council motions ────────────────────────────────────────────────────
//
// Notably: NO `id` column in the TSV. The DB's identity sequence assigns
// IDs on insert. The CouncilVotesPlugin resolves motion IDs via the
// natural key (meeting_id, motion_index) at apply time.

type CouncilMotionsPlugin struct{}

func (CouncilMotionsPlugin) Name() string { return "council_motions" }

func (CouncilMotionsPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "council_motions.tsv"
	cols := []string{"meeting_id", "motion_index", "motion_text", "moved_by", "seconded_by",
		"result", "raw_text", "agenda_item", "significance", "media_url",
		"llm_summary", "llm_label", "llm_significance", "llm_model", "source"}
	query := `SELECT m.meeting_id, m.motion_index, m.motion_text, m.moved_by,
	          m.seconded_by, m.result, m.raw_text, m.agenda_item, m.significance,
	          m.media_url, m.llm_summary, m.llm_label, m.llm_significance, m.llm_model, m.source
	          FROM council_motions m
	          JOIN council_meetings cm ON cm.id = m.meeting_id
	          WHERE cm.term = '2022-2026'
	          ORDER BY cm.date, m.motion_index`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}
	return []Dataset{{
		File: file, Plugin: "council_motions", Table: "council_motions",
		SourceURL:   "https://pub-thunderbay.escribemeetings.com",
		SourceDoc:   "council_meetings.sources.tsv",
		Description: "Council motions 2022-2026 (id auto-assigned on insert)",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "scraped+llm",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (CouncilMotionsPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	header, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging")
	if err != nil {
		return 0, err
	}
	// ON CONFLICT (meeting_id, motion_index) DO NOTHING — natural key dedup.
	return ds.Rows, SimpleUpsert(ctx, tx, ds.Table, "muni_staging", header)
}

// ─── Council vote records ───────────────────────────────────────────────
//
// Resolves motion_id at insert time by joining the staging table to
// council_motions on (meeting_id, motion_index). This makes the dump
// robust regardless of the auto-assigned IDs in the target DB.

type CouncilVotesPlugin struct{}

func (CouncilVotesPlugin) Name() string { return "council_votes" }

func (CouncilVotesPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "council_vote_records.tsv"
	// Natural-key columns instead of motion_id.
	cols := []string{"meeting_id", "motion_index", "councillor", "position", "source"}
	query := `SELECT m.meeting_id, m.motion_index, vr.councillor, vr."position", vr.source
	          FROM council_vote_records vr
	          JOIN council_motions m ON m.id = vr.motion_id
	          JOIN council_meetings cm ON cm.id = m.meeting_id
	          WHERE cm.term = '2022-2026'
	          ORDER BY cm.date, m.motion_index, vr.councillor`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}
	return []Dataset{{
		File: file, Plugin: "council_votes", Table: "council_vote_records",
		SourceURL:   "https://pub-thunderbay.escribemeetings.com",
		SourceDoc:   "council_meetings.sources.tsv",
		Description: "Individual councillor votes 2022-2026 (motion_id resolved at insert)",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "scraped",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (CouncilVotesPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	if _, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging"); err != nil {
		return 0, err
	}
	// JOIN-based INSERT: resolve motion_id from (meeting_id, motion_index).
	_, err := tx.Exec(ctx, `
		INSERT INTO council_vote_records (motion_id, councillor, "position", source)
		SELECT m.id, s.councillor, s.position, s.source
		FROM muni_staging s
		JOIN council_motions m
		  ON m.meeting_id = s.meeting_id
		 AND m.motion_index = s.motion_index::integer
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("INSERT INTO council_vote_records: %w", err)
	}
	return ds.Rows, nil
}

// ─── Budget accounts ────────────────────────────────────────────────────

type BudgetAccountsPlugin struct{}

func (BudgetAccountsPlugin) Name() string { return "budget_accounts" }

func (BudgetAccountsPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "budget_accounts.tsv"
	cols := []string{"code", "name", "type", "parent_code", "color", "sort_order", "source"}
	query := `SELECT code, name, type, parent_code, color, sort_order, source
	          FROM budget_accounts ORDER BY sort_order`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}
	return []Dataset{{
		File: file, Plugin: "budget_accounts", Table: "budget_accounts",
		SourceURL:   "https://thunderbay.ca/en/city-hall/2026-budget.aspx",
		SourceDoc:   "https://thunderbay.ca/en/city-hall/2026-budget.aspx",
		Description: "Chart of accounts FY2026",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "pg_dump",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (BudgetAccountsPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	header, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging")
	if err != nil {
		return 0, err
	}
	return ds.Rows, SimpleUpsert(ctx, tx, ds.Table, "muni_staging", header)
}

// ─── Budget ledger ──────────────────────────────────────────────────────

type BudgetLedgerPlugin struct{}

func (BudgetLedgerPlugin) Name() string { return "budget_ledger" }

func (BudgetLedgerPlugin) Extract(ctx context.Context, pool *pgxpool.Pool, outDir string) ([]Dataset, error) {
	const file = "budget_ledger_2026.tsv"
	cols := []string{"fiscal_year", "debit_code", "credit_code", "amount", "budget_type", "description", "notes", "source", "source_hash"}
	query := `SELECT fiscal_year, debit_code, credit_code, amount::text,
	          budget_type, description, notes, source, source_hash
	          FROM budget_ledger WHERE fiscal_year = 2026 ORDER BY id`

	rows, sha, err := runExtractQuery(ctx, pool, outDir, file, query, cols)
	if err != nil {
		return nil, err
	}
	return []Dataset{{
		File: file, Plugin: "budget_ledger", Table: "budget_ledger",
		SourceURL:   "https://thunderbay.ca/en/city-hall/2026-budget.aspx",
		SourceDoc:   "https://thunderbay.ca/en/city-hall/2026-budget.aspx",
		Description: "Budget ledger FY2026",
		Collected:   time.Now().UTC(), License: "public-record", Processor: "pg_dump",
		Rows: rows, SHA256: sha,
	}}, nil
}

func (BudgetLedgerPlugin) Apply(ctx context.Context, tx pgx.Tx, fsys fs.FS, ds Dataset) (int, error) {
	header, err := LoadStaging(ctx, tx, fsys, ds, "muni_staging")
	if err != nil {
		return 0, err
	}
	return ds.Rows, SimpleUpsert(ctx, tx, ds.Table, "muni_staging", header)
}
