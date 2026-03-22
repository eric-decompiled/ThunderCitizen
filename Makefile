COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -X thundercitizen/internal/handlers.Commit=$(COMMIT) -X thundercitizen/internal/handlers.BuildTime=$(BUILD_TIME)

.PHONY: all dev dev-once generate css css-watch migrate-up migrate-down build clean test test-a11y lint lint-js muni-extract muni-publish

# Run with hot reload (requires air)
dev: node_modules
	air

# Run once without hot reload
dev-once: generate css
	go run -ldflags="$(LDFLAGS)" ./cmd/server

# Generate templ files
generate:
	templ generate

# Build CSS with Sass/Foundation
css: node_modules
	npm run css

# Watch CSS for changes
css-watch: node_modules
	npm run css:watch

# Install npm dependencies if needed
node_modules: package.json
	npm install
	@touch node_modules

# Source data fetching is now done through ./bin/fetcher (interactive).
# See cmd/fetcher/README.md or run `./bin/fetcher` for the menu.

# Run migrations up
migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

# Run migrations down
migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down

# Build every CLI tool into bin/ (gitignored). One command, one place.
# Produces: bin/server, bin/fetcher, bin/summarize, bin/auditbudget,
# bin/buildshapes, bin/gentstypes, bin/perftest, bin/muni, bin/munisign.
#
# Uses a sentinel file (bin/.built) for make incrementality. Re-runs only
# when any tracked Go or SQL source changes; Go's own build cache then
# decides which packages actually need recompiling.
ALL_SOURCES := $(shell find cmd internal templates -name '*.go' 2>/dev/null)

all: bin/.built

bin/.built: $(ALL_SOURCES)
	@mkdir -p bin
	go build -o bin/ ./cmd/...
	@touch bin/.built

# Extract muni data bundle from dev DB
muni-extract: bin/.built
	./bin/muni extract -out data/muni

# Sign + zip + upload muni bundle to DO Spaces
muni-publish: bin/.built
	./bin/munisign sign -key .signing-key.pub data/muni

# Build Docker image
build:
	docker compose build --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME)

# Start all services
up:
	docker compose up

# Start all services in background
up-d:
	docker compose up -d

# Stop all services
down:
	docker compose down

# Clean up
clean:
	docker compose down -v
	rm -f templates/*_templ.go
	rm -f templates/**/*_templ.go
	rm -f static/css/style.css
	rm -f static/css/style.css.map
	rm -rf node_modules

# Run all tests (unit + integration)
test: generate
	go test ./...

# Run integration tests only (requires Postgres)
test-integration:
	go test ./integration/ -v -count=1

# Run accessibility tests (unit; requires running server for view tests)
test-a11y: generate
	go test ./internal/views/ -v -run 'Layout|Budget|Transit'

# Lint everything
lint: lint-js
	go vet ./...

# Lint JavaScript
lint-js: node_modules
	npm run lint:js

# Install development dependencies
deps:
	go install github.com/a-h/templ/cmd/templ@v0.2.793
	go install github.com/air-verse/air@latest
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
