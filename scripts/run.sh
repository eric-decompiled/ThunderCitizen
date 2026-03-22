#!/usr/bin/env bash
set -euo pipefail

# Wrapper for the production compose stack — saves typing
# `-f docker-compose.prod.yml` on every command.
#
# Default assumption: you want the latest images. Every `up` (with or
# without args) pulls first, then runs `up -d` with your flags. The
# no-arg form additionally force-recreates every container and removes
# orphans for a guaranteed-clean rollout.
#
# Usage:
#   ./scripts/run.sh                              # solid rollout: pull + force-recreate everything
#   ./scripts/run.sh up                           # pull + up -d (keeps containers if image unchanged)
#   ./scripts/run.sh up --force-recreate caddy    # pull + targeted recreate
#   ./scripts/run.sh logs -f                      # tail logs
#   ./scripts/run.sh logs -f caddy                # tail one service's logs
#   ./scripts/run.sh ps                           # service status
#   ./scripts/run.sh pull                         # pull only (no up)
#   ./scripts/run.sh down                         # stop everything
#   ./scripts/run.sh exec -T db psql …            # anything else passes through

cd "$(dirname "$0")/.."

dc() {
    docker compose -f docker-compose.prod.yml "$@"
}

log() { printf '\n==> %s\n' "$*"; }

if [[ $# -eq 0 ]]; then
    # Solid rollout: every known failure mode we've hit (stale caddy
    # config, app container created before db, orphaned services after
    # a rename, un-recreated containers after an env change) is fixed
    # by some combination of pull + force-recreate + remove-orphans.
    # Pay the ~10s recreate cost once, always land in the same state.
    log "pulling latest images"
    dc pull --quiet
    log "recreating containers (solid rollout)"
    dc up -d --remove-orphans --force-recreate
    log "status"
    dc ps
    exit 0
fi

# Explicit form — pass user args through but inject sensible defaults:
# every `up` pulls first (so you always get latest) and runs detached.
# `pull` is pull-only — no implicit `up` so you can stage an image fetch
# without restarting anything.
if [[ "$1" == "up" ]]; then
    shift
    log "pulling latest images"
    dc pull --quiet
    log "starting containers"
    dc up -d "$@"
elif [[ "$1" == "pull" ]]; then
    shift
    dc pull "$@"
else
    dc "$@"
fi
