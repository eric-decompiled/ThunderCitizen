# Council Minutes & Votes

## Overview

The council package scrapes Thunder Bay City Council meeting minutes from the eSCRIBE portal, extracts motions and recorded votes, generates LLM summaries, and serves them on the `/minutes` pages. Enriched data is persisted as JSON seed files so the database auto-populates on fresh deployments.

## Data Source

Thunder Bay eSCRIBE portal: `pub-thunderbay.escribemeetings.com`
- **City Council** minutes PDFs (185 meetings, 1,556 motions, 186 recorded votes)
- **Committee of the Whole** agenda pages (report attachments)
- Coverage: 2018–present (two full terms: 2018-2022, 2022-2026)

---

## After a New Council Meeting

When new minutes are published on eSCRIBE (typically 1-2 weeks after a meeting), run this process to add the new meeting to the site:

### Step 1: Scrape & parse

```bash
# Fetch new meetings from eSCRIBE, download PDFs, parse votes, save to DB
./bin/fetcher votes
```

Interactive: previews the meeting URLs and waits for `[y/N]`. Idempotent — existing meetings are updated, new ones are added.

### Step 2: Summarize new motions

```bash
# Generate LLM summaries for any unsummarized motions
ANTHROPIC_API_KEY="..." go run ./cmd/summarize
```

Only processes motions with empty `llm_summary`. Uses Haiku for cost efficiency (~$0.01 per meeting). Also generates meeting-level summaries (synthesizing all motions per meeting). Rate-limited to 45 RPM to stay under the Haiku API limit.

### Step 3: Save summaries to flat file

```bash
go run ./cmd/summarize -dump
```

Exports all LLM summaries to `static/councillors/summaries.json`. **Always run this after summarizing.** This file is the insurance policy — if the DB is ever rebuilt, summaries are restored automatically by the next `./bin/fetcher votes` run.

### Step 4: Tag headline votes (if any)

If any motions had significant media coverage, tag them in the DB:

```sql
UPDATE council_motions SET significance = 'headline',
  media_url = 'https://www.tbnewswatch.com/...'
WHERE id = <motion_id>;
```

Find the motion ID on `/minutes/{meeting_id}` or query the DB. Then re-run `go run ./cmd/summarize -dump` to capture the updated significance.

### Step 5: Republish the muni bundle

```bash
make muni-extract
./bin/munisign sign -key .signing-key.pub data/muni
make muni-publish
git add static/councillors/summaries.json
git commit -m "Update council vote data through <date>"
```

`muni extract` reads the now-enriched dev DB and rewrites `data/muni/councillors.tsv` + `data/muni/council_*.tsv` + `BOD.tsv`. `munisign` signs the bundle, `muni publish` uploads it to DO Spaces. On next boot production fetches the new bundle and applies changed datasets automatically.

---

## CLI Reference

### `./bin/fetcher votes`

Single interactive command. Discovers all council meetings via the eSCRIBE list endpoint, prints the per-meeting PDF URLs, and prompts `[y/N]`. On confirm: downloads PDFs (skipping existing), parses motions, saves meetings/motions/votes to the DB, records provenance, auto-loads `summaries.json` if present, and writes `static/councillors/votes_*.json` per term.

No flags. For programmatic use, call `council.FetchVotes` directly from a small Go shim.

### `cmd/summarize`

```bash
go run ./cmd/summarize                      # unsummarized motions + meetings
go run ./cmd/summarize -force               # re-summarize all
go run ./cmd/summarize -term 2022-2026      # single term
go run ./cmd/summarize -id 284              # single motion
go run ./cmd/summarize -meetings-only       # skip motion pass, only meetings
go run ./cmd/summarize -dry-run             # preview prompts
go run ./cmd/summarize -dump                # export all summaries to summaries.json
go run ./cmd/summarize -load                # import summaries from summaries.json
go run ./cmd/summarize -reformat            # re-paragraph existing meeting summaries
```

Requires `ANTHROPIC_API_KEY` (except `-dump`, `-load`, `-dry-run`). Two-pass design:
1. **Motion pass** — Haiku classifies each motion (summary, label, significance). Override with `-model`.
2. **Meeting pass** — Haiku synthesizes all motions per meeting into a 2-4 sentence summary. Override with `-meeting-model`.

Rate-limited to 45 RPM (1 worker). Cost: ~$0.87 for 1,540 motions, ~$0.83 for 183 meetings (full re-run with `-force`).

## Deployment & Seeding

On server startup, after migrations run:
1. If `council_meetings` is empty, the server loads seed data from `static/councillors/votes_*.json`
2. All meetings, motions, and vote records are inserted
3. If `static/councillors/summaries.json` exists, LLM summaries are auto-loaded into the freshly seeded motions and meetings
4. This entire flow is a no-op if the DB already has data

This means fresh deployments (or DB rebuilds) are fully populated without needing to re-scrape eSCRIBE or re-run the summarizer. The summary flat file is the critical piece — without it, summaries must be regenerated via the Claude API (~$1.70 for a full run).

## Package Structure

```
internal/council/
    store.go                  # Store — DB queries, seeding, vote matrix
    models.go                 # Meeting, Motion, VoteRecord domain types
    scrape.go                 # eSCRIBE API client: list meetings, download PDFs
    parse.go                  # pdftotext extraction + vote parsing (YEA/NAY + inline)
    summarize.go              # Claude API: motion summaries + meeting summaries
```

## Database Tables

- `council_meetings` — meeting metadata, LLM summary (`llm_summary`, `llm_model`), source PDF path
- `council_motions` — every MOVED BY block with full text, agenda heading, LLM summary/label/significance, full-text search (tsvector + trigram)
- `council_vote_records` — per-councillor for/against/absent for recorded votes. PK is `(motion_id, councillor)`; inserts use `ON CONFLICT DO NOTHING` to handle edge cases from consent agenda blocks

## Significance Tiers

Low cardinality column on `council_motions`:
- `headline` — major policy decisions affecting many residents (budget approvals, facility decisions, shelter village)
- `notable` — contentious (close margin, defeated, tied), substantive policy change, significant spending
- `routine` — standard business, committee minutes adoptions, board appointments
- `procedural` — agenda confirmations, adjournments, "continue past 11pm"

Classification is done via a hybrid approach (see [docs/summarize-motions.md](summarize-motions.md)):
1. SQL heuristics classify ~95% of motions as procedural/routine by text patterns
2. Motions with recorded votes are classified by Claude Code sub-agents based on vote context
3. `llm_significance` stores the LLM suggestion when using `cmd/summarize`; the authoritative `significance` column is promoted automatically

The vote matrix on the councillors page only shows `headline` and `notable` motions — procedural/routine items with recorded votes are filtered out.

## Vote Parsing

Thunder Bay's minutes PDFs use two different recorded vote formats. The parser handles both:

### Format 1: YEA/NAY column table (pre-Sep 2025)

Triggered by the phrase "recorded vote was requested". The parser finds the YEA/NAY header, determines column positions, and reads councillor names from left (yea) and right (nay) columns. Handles both side-by-side names (2018 era) and separate-line names (2022 era).

### Format 2: Inline "For (N):" list (Sep 2025+)

No "recorded vote" trigger phrase. Instead, every motion has an inline roll call:
```
For (10): Mayor Ken Boshcoff, Councillor Rajni Agarwal, ...
Against (1): Councillor Michael Zussino
Absent (3): Councillor Mark Bentz, ...
CARRIED (10 to 1)
```

The parser (`attachInlineVotes`) scans each motion block for `For (N):` lines, splits comma/and-separated names, and stops at `CARRIED`/`LOST` or the next `For (` (which would indicate a second vote section from a consent agenda). Results are deduplicated since consent agenda blocks may contain multiple vote sections.

### Name normalization

Councillor names are normalized to canonical form via `knownNames` map in `parse.go` (e.g., "K. Boshcoff" → "Ken Boshcoff", "Shelby Ch'ng" with smart quotes → straight apostrophe). The `cleanName` function strips titles, trims punctuation, and rejects strings that look like motion text rather than names.

## LLM Summarization

### Motion summaries

Each motion is sent to Claude with its text, agenda item, result, and vote count. The model returns JSON with three fields:
- `summary` — 1-2 sentence plain-language summary
- `label` — short label under 60 chars for grid display
- `significance` — classification: headline, notable, routine, procedural

**System prompt**: "You summarize Thunder Bay City Council motions for a civic transparency website. Your audience is residents who want to understand what their council decided without reading legalese."

### Meeting summaries

After motions are summarized, each meeting's motions (with their labels, summaries, results, and significance) are sent to Claude for a 2-4 sentence meeting-level summary. The prompt instructs the model to lead with the most consequential decisions, name specific topics/amounts/locations, and note any defeated or contentious motions. Formulaic openings like "Council met on [date]" are explicitly prohibited.

**System prompt**: Emphasizes plain text, no bullet points, concrete substance over formula. Procedural-only meetings get a single sentence.

### Transparency

The minutes page displays a disclaimer stating that summaries are AI-generated by Claude (claude-haiku-4-5-20251001), describing the input/output methodology and directing users to official minutes for authoritative records.

## Pages

- `/councillors` — council members by term with client-side switching; vote stats (dissent rate, for/against), notable votes table, vote matrix with sticky photo headers and animated detail modal, interactive ward map with Esri satellite tiles and ward descriptions
- `/minutes` — meeting list with term selector, filter chips (All / Recorded Votes / Defeated), HTMX pagination
- `/minutes/{id}` — meeting detail with all motions; procedural items collapsed, substantive motions with summary, clause breakdown, vote details
- `/motions` — full-text search across all motions with term, significance, and result filters

## Storage Layout

```
static/councillors/
    minutes/                  # Archived council minutes PDFs (gitignored)
    reports/                  # Archived report PDFs with SHA-256 (gitignored)
    votes_2018.json           # Vote seed data: meetings, motions, vote records
    votes_2022.json           # Vote seed data: meetings, motions, vote records
    summaries.json            # LLM summaries flat file (1556 motions + 185 meetings)
```

**`summaries.json`** is keyed by `(meeting_id, agenda_item)` for motions and `meeting_id` for meetings. Contains `llm_summary`, `llm_label`, `llm_significance`, `llm_model`, manual `significance` overrides, and `media_url`. Auto-loaded on DB seed; also loadable manually via `go run ./cmd/summarize -load`.

## Meeting List UI

Each meeting row on `/minutes` displays:
- AI-generated meeting summary (prose, with paragraph breaks)
- Date and motion count
- Significance badges: headline (blue), notable (amber) — counts of motions at each tier
- Recorded vote count badge
- Defeated motion count badge
- "See Minutes" link (internal meeting detail page)
- "Official Minutes ↗" link (external eSCRIBE PDF, opens in new tab)
