#!/usr/bin/env bash
set -euo pipefail

# Deploy to the production droplet.
#
# SSHes into the `tc` host, pulls latest main, pulls the new app image,
# and restarts the app container. The db and caddy services are left alone.
#
# Usage:
#   ./scripts/deploy.sh              # deploy HEAD of main
#   ./scripts/deploy.sh --no-pull    # skip `docker compose pull` (use local image)
#   HOST=other ./scripts/deploy.sh   # override ssh host alias

HOST="${HOST:-tc}"
REMOTE_DIR="${REMOTE_DIR:-/opt/ThunderCitizen}"
COMPOSE="docker compose -f docker-compose.prod.yml"

PULL_IMAGE=1
for arg in "$@"; do
    case "$arg" in
        --no-pull) PULL_IMAGE=0 ;;
        -h|--help)
            sed -n '3,12p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) printf 'unknown flag: %s\n' "$arg" >&2; exit 2 ;;
    esac
done

log() { printf '\n==> %s\n' "$*"; }

log "deploying to ${HOST}:${REMOTE_DIR}"

ssh -t "$HOST" "REMOTE_DIR='${REMOTE_DIR}' COMPOSE='${COMPOSE}' PULL_IMAGE='${PULL_IMAGE}' bash -s" <<'REMOTE'
set -euo pipefail

cd "$REMOTE_DIR"

echo "==> git pull"
git pull --ff-only

if [[ "$PULL_IMAGE" == "1" ]]; then
    echo "==> $COMPOSE pull app"
    $COMPOSE pull app
fi

echo "==> $COMPOSE up -d app"
$COMPOSE up -d app

echo "==> status"
$COMPOSE ps app
REMOTE

log "done"
