#!/usr/bin/env bash
set -euo pipefail

# Admin tasks for ThunderCitizen infrastructure.
#
# Subcommands:
#   setup-spaces    Create DO Space + CDN + custom subdomain for data.thundercitizen.ca
#   upload-patches  Alias for scripts/apply.sh
#
# Requires:
#   - DO_API_TOKEN env var (or doctl authenticated — token is read from its config)
#   - s3cmd or aws CLI configured for DO Spaces (for upload)
#   - DO_SPACES_KEY / DO_SPACES_SECRET for Space creation (S3 API)
#
# Usage:
#   DO_SPACES_KEY=... DO_SPACES_SECRET=... ./scripts/admin.sh setup-spaces
#   ./scripts/admin.sh upload-patches

BUCKET="thundercitizen"
REGION="tor1"
CDN_DOMAIN="data.thundercitizen.ca"
SPACES_ENDPOINT="https://${REGION}.digitaloceanspaces.com"
CDN_ORIGIN="${BUCKET}.${REGION}.digitaloceanspaces.com"

cd "$(dirname "$0")/.."

log()  { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- Token resolution ---

resolve_api_token() {
    if [[ -n "${DO_API_TOKEN:-}" ]]; then
        return
    fi
    # Try reading from doctl config
    local doctl_config="${HOME}/.config/doctl/config.yaml"
    if [[ -f "$doctl_config" ]]; then
        DO_API_TOKEN=$(grep 'access-token:' "$doctl_config" | head -1 | awk '{print $2}')
        if [[ -n "$DO_API_TOKEN" ]]; then
            info "using token from doctl config"
            return
        fi
    fi
    die "set DO_API_TOKEN or authenticate doctl"
}

do_api() {
    local method="$1" path="$2"
    shift 2
    curl -fsSL -X "$method" \
        -H "Authorization: Bearer ${DO_API_TOKEN}" \
        -H "Content-Type: application/json" \
        "https://api.digitalocean.com/v2${path}" \
        "$@"
}

# --- setup-spaces ---

cmd_setup_spaces() {
    resolve_api_token

    [[ -z "${DO_SPACES_KEY:-}" ]] && die "set DO_SPACES_KEY (Spaces access key)"
    [[ -z "${DO_SPACES_SECRET:-}" ]] && die "set DO_SPACES_SECRET (Spaces secret key)"

    # 1. Create the Space via S3 PUT bucket
    log "creating Space: ${BUCKET} in ${REGION}"
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" \
        -X PUT "${SPACES_ENDPOINT}/${BUCKET}" \
        -H "Host: ${REGION}.digitaloceanspaces.com" \
        -H "x-amz-acl: public-read" \
        --aws-sigv4 "aws:amz:${REGION}:s3" \
        -u "${DO_SPACES_KEY}:${DO_SPACES_SECRET}")

    case "$status" in
        200) info "created" ;;
        409) info "already exists — continuing" ;;
        *)   die "Space creation failed (HTTP ${status}). Check credentials and region." ;;
    esac

    # 2. Enable CDN on the Space
    log "enabling CDN with custom domain: ${CDN_DOMAIN}"

    # Check if CDN endpoint already exists
    local existing
    existing=$(do_api GET "/cdn/endpoints" | python3 -c "
import sys, json
eps = json.load(sys.stdin).get('endpoints', [])
match = [e for e in eps if e.get('origin') == '${CDN_ORIGIN}']
print(match[0]['id'] if match else '')
" 2>/dev/null) || true

    if [[ -n "$existing" ]]; then
        info "CDN endpoint exists (${existing})"
        info "updating custom domain to ${CDN_DOMAIN}"
        do_api PUT "/cdn/endpoints/${existing}" \
            -d "{\"custom_domain\": \"${CDN_DOMAIN}\", \"certificate_id\": \"\"}" \
            > /dev/null 2>&1 || info "custom domain update may need manual cert — see below"
    else
        local cdn_resp
        cdn_resp=$(do_api POST "/cdn/endpoints" \
            -d "{
                \"origin\": \"${CDN_ORIGIN}\",
                \"ttl\": 3600,
                \"custom_domain\": \"${CDN_DOMAIN}\",
                \"certificate_id\": \"\"
            }" 2>&1) || true

        if echo "$cdn_resp" | python3 -c "import sys,json; e=json.load(sys.stdin).get('endpoint',{}); print(e.get('id',''))" 2>/dev/null | grep -q .; then
            info "CDN endpoint created"
        else
            info "CDN creation response: ${cdn_resp}"
            info "you may need to enable CDN manually in the dashboard"
        fi
    fi

    # 3. DNS instructions
    log "DNS setup required"
    info "add a CNAME record:"
    info "  ${CDN_DOMAIN}  →  ${CDN_ORIGIN}"
    info ""
    info "DO provisions a Let's Encrypt cert automatically once DNS propagates."
    info "verify with: dig CNAME ${CDN_DOMAIN}"

    # 4. Upload initial patches if tool available
    if command -v s3cmd &>/dev/null || command -v aws &>/dev/null; then
        log "uploading initial patches"
        ./scripts/apply.sh
    else
        log "skipping upload — install s3cmd or aws CLI, then run:"
        info "./scripts/apply.sh"
    fi

    log "done"
    info "production URL: https://${CDN_DOMAIN}/patches.zip"
    info "server reads this by default when ENVIRONMENT=production"
}

# --- upload-patches ---

cmd_upload_patches() {
    exec ./scripts/apply.sh "$@"
}

# --- dispatch ---

case "${1:-}" in
    setup-spaces)    shift; cmd_setup_spaces "$@" ;;
    upload-patches)  shift; cmd_upload_patches "$@" ;;
    *)
        cat >&2 <<'USAGE'
usage: admin.sh <command>

commands:
  setup-spaces      Create DO Space + CDN for data.thundercitizen.ca
  upload-patches    Zip + upload patches to DO Spaces
USAGE
        exit 2
        ;;
esac
