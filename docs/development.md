# Development

## Dev Container (Recommended)

The easiest way to get started is with a dev container. It provides Go, Node, Postgres, and all tooling pre-configured.

1. Open the repo in VS Code or Zed
2. Choose "Reopen in Container" (VS Code) or use the dev container panel (Zed)
3. Wait for the container to build and `make deps && npm install` to complete
4. Run `make dev` — server starts on http://localhost:8080

The dev container includes:
- Go 1.25 with templ, air, and migrate
- Node.js LTS (for Sass)
- PostgreSQL 16 (auto-started, no setup needed)
- VS Code extensions: Go, Templ, Sass, SQLTools

Environment variables (`DATABASE_URL`, `PORT`, `ENVIRONMENT`) are pre-configured.

## Local Setup

### Prerequisites

- Go 1.24+
- Node.js (for Sass/Pico CSS)
- PostgreSQL (or use Docker)

### Install Dependencies

```bash
make deps    # Install Go tools: templ, air, migrate
npm install  # Install Sass and Pico CSS
```

## Running Locally

```bash
make dev       # Hot reload (watches .go, .templ, .scss)
make dev-once  # Run without hot reload
```

Requires Postgres running locally or via `docker compose up db`.

## Individual Commands

```bash
make generate  # Generate templ files
make css       # Build CSS (Sass)
make css-watch # Watch CSS for changes
make lint      # Run go vet + ESLint
make lint-js   # ESLint only
```

## Operator Tools

`make all` builds every helper binary into `bin/` (gitignored). Two main CLIs:

```bash
./bin/fetcher              # interactive menu for refreshing source data
./bin/fetcher budget       # Ontario FIR data
./bin/fetcher gtfs         # Thunder Bay GTFS schedule
./bin/fetcher votes        # eSCRIBE council meetings
./bin/fetcher wards        # Open North ward boundaries

./bin/patches extract      # dev DB → patches/*.sql files (regenerate after fetch)
./bin/patches apply        # patches/*.sql → DATABASE_URL (run after deploy)
```

`fetcher` is interactive only — every subcommand previews URLs and prompts `[y/N]` before downloading. See [cmd/fetcher/README.md](../cmd/fetcher/README.md) for the programmatic API if you need a non-interactive entry point.

`patches` is a manual operator tool too. Server boot does **not** auto-apply patches — code deploys and data deploys are deliberately separate. See [patches/README.md](../patches/README.md) for the full lifecycle.

Other binaries in `bin/`: `summarize` (LLM motion classifier), `auditbudget` (sub-ledger balance check), `buildshapes` (route shapes from GTFS), `gentstypes` (TS interfaces from Go API structs), `perftest` (latency report).

## What `make dev` Does

Air handles hot reload and runs these pre-build commands:

1. `gofmt -w ./internal ./cmd` - Format Go code
2. `templ generate` - Compile `.templ` to Go
3. `npm run css` - Compile SCSS to CSS

## Testing

### Unit Tests

```bash
go test ./...                      # Run all tests
go test ./templates/components/    # Run component tests only
```

Component tests (`templates/components/components_test.go`) verify Templ components render correctly.

### Visual Testing

Visual testing uses Playwright MCP for browser automation.

**Setup** (one-time):
1. Create `~/.claude/mcp.json`:
   ```json
   {
     "mcpServers": {
       "playwright": {
         "command": "npx",
         "args": ["@playwright/mcp@latest"]
       }
     }
   }
   ```
2. Restart Claude Code

**Usage**: Run `make dev`, then use Playwright tools to navigate pages and capture screenshots.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable` | Postgres connection |
| `PORT` | `8080` | Server port |
| `ENVIRONMENT` | `development` | Environment name |
