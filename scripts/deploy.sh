#!/usr/bin/env bash
set -euo pipefail

# Deploy on the droplet. Idempotent.
# - Cold boot (stack not fully up): pulls images, brings everything up.
# - Hot redeploy (db + caddy already running): pulls the app image and
#   recreates only the app container. db and caddy are left alone so
#   downtime is just the app restart.
#
# Usage:
#   ./scripts/deploy.sh            # git pull, then act on current state
#   ./scripts/deploy.sh --no-git   # skip git pull
#
# Run from your laptop:
#   ssh tc '/opt/ThunderCitizen/scripts/deploy.sh'

cd "$(dirname "$0")/.."

COMPOSE="docker compose -f docker-compose.prod.yml"
GIT_PULL=1

for arg in "$@"; do
    case "$arg" in
        --no-git) GIT_PULL=0 ;;
        -h|--help) sed -n '3,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) printf 'unknown flag: %s\n' "$arg" >&2; exit 2 ;;
    esac
done

log() { printf '\n==> %s\n' "$*"; }

if [[ "$GIT_PULL" == "1" ]] && [ -d .git ]; then
    log "git pull"
    git pull --ff-only
fi

running=$($COMPOSE ps --services --filter status=running 2>/dev/null || true)

if grep -qx db <<<"$running" && grep -qx caddy <<<"$running"; then
    log "stack is up — bouncing app only"
    $COMPOSE pull --quiet app
    $COMPOSE up -d --no-deps --force-recreate --quiet-pull app
else
    log "cold boot — bringing full stack up"
    $COMPOSE pull --quiet
    $COMPOSE up -d --remove-orphans
fi

$COMPOSE ps
