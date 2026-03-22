#!/usr/bin/env bash
set -euo pipefail

# Basic hardening for a fresh Debian droplet running ThunderCitizen.
#
# Idempotent — safe to re-run. Performs the steps that are easy to get
# wrong by hand:
#
#   - apt update + dist-upgrade
#   - install ufw, fail2ban, unattended-upgrades
#   - configure ufw to allow SSH, 80, 443 only; enable
#   - configure fail2ban with an sshd jail (1h ban after 5 fails in 10m)
#   - enable unattended security upgrades
#   - set system timezone to UTC (so logs/dumps are timestamped consistently)
#
# Things this script intentionally does NOT do:
#   - Touch /etc/ssh/sshd_config. DO droplets created with SSH key auth
#     already have PasswordAuthentication off. If you want to harden SSH
#     further (port move, AllowUsers, etc.) do it yourself.
#   - Create a non-root user. The compose stack runs as root because
#     docker requires it; a separate non-root user adds operational
#     friction with no real benefit on a single-purpose box.
#   - Install docker. Use `curl -fsSL https://get.docker.com | sh` for
#     that — see DEPLOY-DROPLET.md.
#
# Usage:
#   sudo ./scripts/harden.sh

# --- Preconditions ---

if [[ $EUID -ne 0 ]]; then
    echo "error: must run as root (try: sudo $0)" >&2
    exit 1
fi

if ! [[ -f /etc/debian_version ]]; then
    echo "error: this script targets Debian-family systems only" >&2
    exit 1
fi

export DEBIAN_FRONTEND=noninteractive

log() { echo "==> $*"; }

# --- System update ---

log "apt update + dist-upgrade"
apt-get update -qq
apt-get -y -qq dist-upgrade

# --- Packages ---

log "installing ufw, fail2ban, unattended-upgrades"
apt-get install -y -qq \
    ufw \
    fail2ban \
    unattended-upgrades \
    ca-certificates \
    curl

# --- Firewall ---

log "configuring ufw (allow SSH, 80, 443)"
# Set defaults idempotently
ufw --force default deny incoming  >/dev/null
ufw --force default allow outgoing >/dev/null
# Allow rules. `ufw allow` is idempotent.
ufw allow OpenSSH >/dev/null
ufw allow 80/tcp  >/dev/null
ufw allow 443/tcp >/dev/null
# Enable (no-op if already active).
ufw --force enable >/dev/null

# --- fail2ban ---

log "configuring fail2ban sshd jail"
# Write jail.local rather than editing jail.conf so package upgrades don't
# clobber our overrides.
cat > /etc/fail2ban/jail.local <<'EOF'
[DEFAULT]
bantime  = 1h
findtime = 10m
maxretry = 5
backend  = systemd

[sshd]
enabled = true
EOF
systemctl enable --now fail2ban >/dev/null
systemctl restart fail2ban

# --- Unattended security upgrades ---

log "enabling unattended security upgrades"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF

# --- Timezone ---

log "setting timezone to UTC"
timedatectl set-timezone UTC

# --- Summary ---

echo
log "done. summary:"
echo
ufw status verbose | sed 's/^/    /'
echo
fail2ban-client status sshd 2>/dev/null | sed 's/^/    /' || true
echo
echo "    timezone: $(timedatectl show -p Timezone --value)"
echo
echo "Next: install docker"
echo "    curl -fsSL https://get.docker.com | sh"
