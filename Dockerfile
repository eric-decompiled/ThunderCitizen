# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the ThunderCitizen server + operator tools.
#
#   builder  golang:1.24-bookworm  → produces every Go binary we ship
#   runtime  debian:bookworm-slim  → just the binaries + static assets + migrations
#
# Data patches (patches/*.sql) are NOT bundled — the server downloads them
# at boot from PATCHES_URL (a zip on a GitHub release). The patches CLI
# (cmd/patches) is for local dev only and is NOT built here.
# Schema migrations ARE bundled as files because golang-migrate reads
# them from disk on startup (cmd/server/main.go: migrate.New("file://migrations", ...)).

# ─────────────────────────────────────────────────────────────────────
# Builder
# ─────────────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS builder

# Node + npm for SCSS and TypeScript bundling. nodejs in bookworm is 18.x
# which is enough for our esbuild + sass + tsc usage.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        nodejs \
        npm \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# templ codegen — pin to the same version Makefile/CLAUDE.md uses.
RUN go install github.com/a-h/templ/cmd/templ@v0.2.793

# Go module cache layer — only invalidates when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# npm dependency layer — only invalidates when package*.json change.
COPY package.json package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

# Source last so the heavy dep layers above stay cached.
COPY . .

# Codegen + asset build. templ → *_templ.go, sass → static/css/style.css,
# esbuild → static/transit/transit-map.js. All written into the build
# context so the runtime stage can pick them up below.
RUN templ generate \
 && npm run css \
 && npm run build:js

# Build every CLI we ship. patches and gentstypes are deliberately
# excluded — patches is applied out-of-band, gentstypes is dev-only
# codegen for the api.gen.ts file.
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out; \
    LDFLAGS="-w -s \
        -X thundercitizen/internal/handlers.Commit=${COMMIT} \
        -X thundercitizen/internal/handlers.BuildTime=${BUILD_TIME}"; \
    for pkg in server fetcher summarize auditbudget buildshapes perftest; do \
        CGO_ENABLED=0 GOOS=linux go build \
            -trimpath \
            -ldflags="${LDFLAGS}" \
            -o "/out/${pkg}" \
            "./cmd/${pkg}"; \
    done

# ─────────────────────────────────────────────────────────────────────
# Runtime
# ─────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        wget \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 1001 app \
 && useradd  --system --uid 1001 --gid app --home /app --shell /usr/sbin/nologin app

WORKDIR /app

# Binaries land in /usr/local/bin so every tool is on PATH:
#   server, fetcher, summarize, auditbudget, buildshapes, perftest
COPY --from=builder /out/ /usr/local/bin/

# Runtime assets. Migrations are read by golang-migrate at server start
# via "file://migrations" — they MUST sit next to WORKDIR. Templates are
# compiled into the binary by templ, so we don't ship templates/.
COPY --from=builder /src/static     /app/static
COPY --from=builder /src/migrations /app/migrations

RUN chown -R app:app /app

USER app

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://localhost:8080/health | grep -qx OK || exit 1

ENTRYPOINT ["/usr/local/bin/server"]
