#!/usr/bin/env bash
set -euo pipefail

# ThunderCitizen database backup: thin pg_dump wrapper that works against
# both dev (docker-compose.yml) and prod (docker-compose.prod.yml) stacks.
#
# Runs pg_dump inside the db container via `docker compose exec`. The dump
# is written to /backups inside the container — bind-mounted from
# ./backups on the host. Output filenames are
# `thundercitizen-<UTC ISO timestamp>.sql.gz` so plain `ls` sorts them
# chronologically.
#
# Usage:
#   ./scripts/backup.sh              # defaults to prod compose
#   ./scripts/backup.sh --dev        # targets the dev compose stack
#
# Environment:
#   COMPOSE_FILE  compose file to target (overrides --dev flag)
#   DB_NAME       database name           (default: thundercitizen)
#   DB_USER       database user           (default: thundercitizen, --dev overrides to postgres)

DEV=false
for arg in "$@"; do
    case "$arg" in
        --dev) DEV=true ;;
        *) echo "usage: $0 [--dev]" >&2; exit 2 ;;
    esac
done

if [[ "$DEV" == true ]]; then
    COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"
    DB_USER="${DB_USER:-postgres}"
else
    COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.prod.yml}"
    DB_USER="${DB_USER:-thundercitizen}"
fi
DB_NAME="${DB_NAME:-thundercitizen}"
TIMESTAMP=$(date -u +%Y-%m-%dT%H-%M-%SZ)

cd "$(dirname "$0")/.."

# --- Validation ---

if ! command -v docker &>/dev/null; then
    echo "error: docker not found" >&2
    exit 1
fi

if [[ ! -f "$COMPOSE_FILE" ]]; then
    echo "error: compose file not found: $COMPOSE_FILE" >&2
    exit 1
fi

dc() {
    docker compose -f "$COMPOSE_FILE" "$@"
}

if ! dc ps --services --status running 2>/dev/null | grep -qx db; then
    echo "error: db service is not running in $COMPOSE_FILE" >&2
    exit 1
fi

if ! dc exec -T db test -d /backups; then
    echo "error: /backups is not mounted inside the db container" >&2
    echo "       check $COMPOSE_FILE has './backups:/backups' on the db service" >&2
    exit 1
fi

# --- Dump ---

dump_in_container="/backups/thundercitizen-${TIMESTAMP}.sql.gz"
dump_on_host="./backups/thundercitizen-${TIMESTAMP}.sql.gz"

echo "==> dumping ${DB_NAME} → ${dump_on_host} (${COMPOSE_FILE})"

if ! dc exec -T db pg_dump -w -U "${DB_USER}" -Z gzip:9 -f "${dump_in_container}" "${DB_NAME}"; then
    echo "error: pg_dump failed — removing partial file" >&2
    dc exec -T db rm -f "${dump_in_container}" 2>/dev/null || true
    exit 1
fi

if [[ ! -s "$dump_on_host" ]]; then
    echo "error: dump file is empty or missing on host: $dump_on_host" >&2
    exit 1
fi

size=$(du -h "$dump_on_host" | cut -f1)
echo "==> done: ${dump_on_host} (${size})"
