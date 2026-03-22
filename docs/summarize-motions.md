# Classify Council Motions

Classify all council motions by significance, then persist results to seed files.

## Prerequisites

- Postgres running with `thundercitizen` database seeded

## Process

### 1. Check current state

```bash
psql "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable" -c "
SELECT significance, count(*), count(*) FILTER (WHERE raw_text != '') as with_votes
FROM council_motions GROUP BY significance ORDER BY count(*) DESC;"
```

If all motions have significance set, skip to step 4.

### 2. Bulk-classify obvious patterns with SQL

Most motions can be classified by text patterns without any LLM:

```sql
-- Procedural: agenda confirmations, adjournments, previous minutes
UPDATE council_motions SET significance = 'procedural'
WHERE significance = ''
  AND (
    lower(motion_text) LIKE '%confirmation of the agenda%'
    OR lower(motion_text) LIKE '%confirm the agenda%'
    OR lower(motion_text) LIKE '%previous meeting%be confirmed%'
    OR lower(motion_text) LIKE '%meeting do now adjourn%'
    OR lower(motion_text) LIKE '%continue past%'
    OR lower(motion_text) LIKE '%proceed past%'
    OR lower(agenda_item) LIKE '%confirmation of agenda%'
    OR lower(agenda_item) LIKE '%adjournment%'
    OR lower(agenda_item) LIKE '%confirmation of previous%'
  );

-- Routine: committee minutes, by-law readings, ward minutes (without recorded votes)
UPDATE council_motions SET significance = 'routine'
WHERE significance = '' AND raw_text = ''
  AND (
    lower(motion_text) LIKE '%committee of the whole%be adopted%'
    OR lower(motion_text) LIKE '%committee of the whole%be received%'
    OR lower(motion_text) LIKE '%ward meeting minutes%'
    OR lower(motion_text) LIKE '%by-law%be read%'
    OR lower(motion_text) LIKE '%minutes of the%ward%'
  );

-- Everything else without a recorded vote → routine
UPDATE council_motions SET significance = 'routine'
WHERE significance = '' AND raw_text = '';
```

This leaves only motions with recorded votes (~78) unclassified.

### 3. Classify recorded-vote motions

These are the motions where a councillor requested a roll call — they're inherently more significant. Spawn sub-agents (one per term) to classify them.

**Query the remaining motions:**

```bash
psql "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable" -t -A -F '|||' -c "
SELECT mo.id, m.term, m.date,
  COALESCE(NULLIF(mo.agenda_item,''), LEFT(mo.motion_text, 150)),
  mo.result,
  (SELECT count(*) FROM council_vote_records r WHERE r.motion_id = mo.id AND r.position = 'for'),
  (SELECT count(*) FROM council_vote_records r WHERE r.motion_id = mo.id AND r.position = 'against'),
  COALESCE(mo.llm_summary, LEFT(mo.motion_text, 200))
FROM council_motions mo
JOIN council_meetings m ON m.id = mo.meeting_id
WHERE mo.significance = '' AND mo.raw_text != ''
ORDER BY m.term, m.date;"
```

**Spawn two sub-agents**, one per term. Give each the list of motion IDs with their summary, result, and vote counts. Have them classify as:

- **headline**: Major policy (budget approvals, facility decisions, shelter village, police spending)
- **notable**: Contentious (close margin, defeated, tie), significant spending, policy change
- **routine**: Standard business that happened to get a recorded vote (committee minutes, appointments)

Each agent runs batch SQL updates:
```sql
UPDATE council_motions SET significance = 'headline' WHERE id IN (...);
UPDATE council_motions SET significance = 'notable' WHERE id IN (...);
-- Leave the rest as routine:
UPDATE council_motions SET significance = 'routine' WHERE id IN (...);
```

### 4. Verify results

```bash
psql "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable" -c "
SELECT significance, count(*)
FROM council_motions
GROUP BY significance
ORDER BY count(*) DESC;"
```

Expected distribution (approximate):
- `routine`: ~80% (standard business, uncontested)
- `procedural`: ~5% (agenda confirmations, adjournments)
- `notable`: ~10% (contested votes, significant spending)
- `headline`: ~2-5% (major policy decisions)

### 5. Regenerate the council patch

Write the enriched DB state back into the patch SQL file that ships with the binary:

```bash
./bin/patches extract
```

This updates `patches/0001_council_2022-2026.sql`. Commit it.

### 6. Commit

```bash
git add patches/0001_council_2022-2026.sql static/councillors/summaries.json
git commit -m "Update council vote data with significance classifications"
```

## Optional: LLM enrichment

If you have an `ANTHROPIC_API_KEY` with credits, you can also re-generate summaries and labels using Claude Haiku (~$1.50 for all motions):

```bash
go run ./cmd/summarize -force
./bin/patches extract
```

This upgrades the scraper-generated summaries to cleaner plain-language text and adds short labels for grid display.

## What significance enables

Once motions have significance tags:

- **Councillor notable votes**: Each councillor's profile shows their votes on headline/notable motions
- **Key votes table**: The councillors page shows DB-sourced headline votes with links to minutes
- **Motion search filters**: The /motions page can filter by significance tier
- **Vote matrix key indicators**: Star icons appear next to headline/notable votes in the matrix

## Re-running after new scrapes

After scraping new meetings with `./bin/fetcher votes`, new motions will have empty significance. Re-run steps 2-6 above. The SQL patterns handle the bulk; only new recorded-vote motions need agent classification.
