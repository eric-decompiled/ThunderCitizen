# fetcher

Manual operator CLI for refreshing curated source data. One binary, four subcommands, no flags.

```
fetcher              # interactive menu
fetcher budget       # Ontario FIR data (all missing fiscal years)
fetcher gtfs         # Thunder Bay GTFS static schedule
fetcher votes        # eSCRIBE council meetings (all terms)
fetcher wards        # Open North ward boundaries
```

## How it works

Every subcommand follows the same flow:

1. **Discover** — call the upstream's cheap "list" endpoint (no heavy downloads). For `gtfs` this is just a single hardcoded URL; for `votes` it's the eSCRIBE meeting pagination; for `budget` it's a per-year availability probe; for `wards` it's the Open North list endpoint.
2. **Print** — show every URL that would be downloaded, numbered.
3. **Prompt** — `Proceed? [y/N]:` on the terminal. Anything other than `y`/`yes` cancels.
4. **Fetch** — only after explicit confirmation: download, parse, write to disk and/or DB.

The CLI requires a real terminal. If stdin isn't a TTY (cron, CI, piped input), it errors with a clear message — this is by design. Manual operator use only.

## What lands where

| Subcommand | Static files | Database tables |
|---|---|---|
| `budget` | `static/budget/fir_<year>.json` | (none — server reads the JSON at startup) |
| `gtfs`   | `static/transit/gtfs/*.txt` | (none — server reloads on next 4h tick) |
| `votes`  | `static/councillors/votes_*.json`, `static/councillors/minutes/*.pdf` | `council_meetings`, `council_motions`, `council_vote_records` |
| `wards`  | `static/councillors/thunder-bay-wards.geojson` | (none) |

Only `votes` writes to the database. `DATABASE_URL` is read from `secrets.conf` (or the env) and is required for that subcommand; the others work offline.

## Programmatic / scheduled use

If you need to run a fetch from cron or a script, **don't try to drive `fetcher`** — it'll refuse to run without a TTY. Instead, write a small Go shim that calls the underlying internal package directly:

```go
import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
    "thundercitizen/internal/budget"
)

func nightlyBudgetRefresh(ctx context.Context, pool *pgxpool.Pool) error {
    return budget.FetchFIR(ctx, budget.FIRFetchOptions{}, pool)
}
```

The four packages exposing `Fetch*` and `Discover*Sources` functions:

- `internal/budget` — `DiscoverFIRSources`, `FetchFIR`, `FIRFetchOptions{Year}`
- `internal/transit` — `DiscoverGTFSSources`, `FetchGTFS`
- `internal/council` — `DiscoverVoteSources`, `FetchVotes`, `ExportVotes`, `VotesFetchOptions{Term, SkipDownload}`
- `internal/wards` — `DiscoverSources`, `Fetch`

These are the same functions the CLI subcommands call. Anything more complex than "fetch the latest of everything" goes through them, not the CLI.

## After fetching

Source data lives in the dev DB and on disk. To ship it to production, regenerate the patches:

```bash
./bin/patches extract     # dev DB → patches/*.sql
git add patches/*.sql static/...
git commit
```

Then deploy the new image and `./bin/patches apply` on the production DB.
