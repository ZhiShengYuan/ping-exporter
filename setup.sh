#!/usr/bin/env bash
# setup.sh — install ping-exporter as a systemd service in privileged (ICMP) mode.
#
# Usage:
#   sudo ./setup.sh --bin-url <release-base-url> --config-url <config-url> [OPTIONS]
#
# Required:
#   --bin-url    <url>  Base URL of the GitHub release.
#                       The script appends the arch suffix automatically, e.g.:
#                         https://github.com/ZhiShengYuan/ping-exporter/releases/download/v1.0.0
#   --config-url <url>  URL the exporter polls for its JSON config.
#
# Optional:
#   --listen-addr   <addr>  HTTP listen address (default: :9427)
#   --poll-interval <dur>   Config poll interval (default: 30s)
#   --install-dir   <dir>   Directory to install binary (default: /usr/local/bin)
#   --help                  Show this message and exit.
#
# Privileges:
#   Uses systemd DynamicUser + AmbientCapabilities=CAP_NET_RAW.
#   No system user is created; no setcap is required.
#   Requires systemd >= 232 (2016).

set -euo pipefail

# ── colour helpers ────────────────────────────────────────────────────────────
RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# ── argument parsing ──────────────────────────────────────────────────────────
BIN_URL=""
CONFIG_URL=""
LISTEN_ADDR=":9427"
POLL_INTERVAL="30s"
INSTALL_DIR="/usr/local/bin"

usage() {
  grep '^#' "$0" | grep -v '#!/' | sed 's/^# \{0,1\}//'
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin-url)        BIN_URL="$2";        shift 2 ;;
    --config-url)     CONFIG_URL="$2";     shift 2 ;;
    --listen-addr)    LISTEN_ADDR="$2";    shift 2 ;;
    --poll-interval)  POLL_INTERVAL="$2";  shift 2 ;;
    --install-dir)    INSTALL_DIR="$2";    shift 2 ;;
    --help|-h)        usage ;;
    *) error "Unknown argument: $1.  Run with --help for usage." ;;
  esac
done

[[ -z "$BIN_URL"    ]] && error "--bin-url is required."
[[ -z "$CONFIG_URL" ]] && error "--config-url is required."

# ── root check ────────────────────────────────────────────────────────────────
[[ "$EUID" -ne 0 ]] && error "This script must be run as root (sudo)."

# ── systemd version check ─────────────────────────────────────────────────────
SYSTEMD_VER=$(systemctl --version | awk 'NR==1{print $2}')
if [[ "$SYSTEMD_VER" -lt 232 ]]; then
  error "systemd >= 232 required for DynamicUser (found ${SYSTEMD_VER})."
fi

# ── detect architecture ───────────────────────────────────────────────────────
MACHINE=$(uname -m)
case "$MACHINE" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) error "Unsupported architecture: $MACHINE. Only amd64 and arm64 are available." ;;
esac
info "Detected architecture: ${ARCH}"

# ── download binary ───────────────────────────────────────────────────────────
BASE_URL="${BIN_URL%/}"
DOWNLOAD_URL="${BASE_URL}/ping-exporter-linux-${ARCH}"
BINARY="${INSTALL_DIR}/ping-exporter"

info "Downloading binary from: ${DOWNLOAD_URL}"
if command -v curl &>/dev/null; then
  curl -fsSL --retry 3 -o "${BINARY}" "${DOWNLOAD_URL}"
elif command -v wget &>/dev/null; then
  wget -q --tries=3 -O "${BINARY}" "${DOWNLOAD_URL}"
else
  error "Neither curl nor wget found. Please install one and retry."
fi
chmod +x "${BINARY}"
info "Binary installed to: ${BINARY}"

# ── write systemd unit file ───────────────────────────────────────────────────
# DynamicUser=yes  — systemd allocates a transient UID at start, no useradd needed.
# AmbientCapabilities=CAP_NET_RAW — injected by systemd before exec; raw ICMP works
#   without setcap or root. Requires NoNewPrivileges=true to be effective.
# CapabilityBoundingSet=CAP_NET_RAW — drop all other capabilities.

UNIT_FILE="/etc/systemd/system/ping-exporter.service"
cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=Ping Exporter — Prometheus ICMP/TCP latency exporter
Documentation=https://github.com/ZhiShengYuan/ping-exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
DynamicUser=yes
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true
ExecStart=${BINARY} \\
  --config.url=${CONFIG_URL} \\
  --config.poll-interval=${POLL_INTERVAL} \\
  --web.listen-address=${LISTEN_ADDR} \\
  --ping.privileged=true
Restart=on-failure
RestartSec=5s
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

info "Systemd unit written to: ${UNIT_FILE}"

# ── enable and start service ──────────────────────────────────────────────────
systemctl daemon-reload
systemctl enable ping-exporter
systemctl restart ping-exporter

sleep 1
STATUS=$(systemctl is-active ping-exporter || true)
if [[ "$STATUS" == "active" ]]; then
  info "Service started successfully."
else
  warn "Service status: ${STATUS}. Check logs with: journalctl -u ping-exporter -n 50"
fi

# ── summary ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  ping-exporter installed successfully${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo "  Binary      : ${BINARY}"
echo "  Config URL  : ${CONFIG_URL}"
echo "  Metrics     : http://localhost${LISTEN_ADDR}/metrics"
echo "  Service user: transient (systemd DynamicUser)"
echo "  Privilege   : CAP_NET_RAW via systemd AmbientCapabilities"
echo ""
echo "  Useful commands:"
echo "    systemctl status ping-exporter"
echo "    journalctl -u ping-exporter -f"
echo "    systemctl restart ping-exporter"
echo ""
