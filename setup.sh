#!/usr/bin/env bash
# setup.sh — install ping-exporter as a systemd service in privileged (ICMP) mode.
#
# Usage:
#   sudo ./setup.sh --bin-url <release-base-url> --config-url <config-url> [OPTIONS]
#
# Required:
#   --bin-url   <url>   Base URL of the GitHub release.
#                       The script appends the arch suffix automatically, e.g.:
#                         https://github.com/ZhiShengYuan/ping-exporter/releases/download/v1.0.0
#   --config-url <url>  URL the exporter polls for its JSON config.
#
# Optional:
#   --listen-addr  <addr>  HTTP listen address (default: :9427)
#   --poll-interval <dur>  Config poll interval (default: 30s)
#   --install-dir   <dir>  Directory to install binary (default: /usr/local/bin)
#   --help                 Show this message and exit.
#
# Privileges:
#   The script grants CAP_NET_RAW to the binary via setcap so it can send raw
#   ICMP without running the service as root.  If setcap is unavailable it falls
#   back to User=root in the unit file.

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

# ── detect architecture ───────────────────────────────────────────────────────
MACHINE=$(uname -m)
case "$MACHINE" in
  x86_64)          ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
  *) error "Unsupported architecture: $MACHINE. Only amd64 and arm64 are available." ;;
esac
info "Detected architecture: ${ARCH}"

# ── construct download URL ────────────────────────────────────────────────────
# Strip trailing slash then append binary name.
BASE_URL="${BIN_URL%/}"
DOWNLOAD_URL="${BASE_URL}/ping-exporter-linux-${ARCH}"
BINARY="${INSTALL_DIR}/ping-exporter"

# ── download binary ───────────────────────────────────────────────────────────
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

# ── create dedicated system user ──────────────────────────────────────────────
SERVICE_USER="ping-exporter"
if ! id -u "${SERVICE_USER}" &>/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
  info "Created system user: ${SERVICE_USER}"
else
  info "System user already exists: ${SERVICE_USER}"
fi

# ── grant CAP_NET_RAW (raw ICMP) ─────────────────────────────────────────────
USE_ROOT=false
if command -v setcap &>/dev/null; then
  if setcap cap_net_raw+ep "${BINARY}"; then
    info "Granted CAP_NET_RAW to ${BINARY} via setcap."
  else
    warn "setcap failed; falling back to running service as root."
    USE_ROOT=true
  fi
else
  warn "setcap not found (install libcap2-bin / libcap); falling back to running service as root."
  USE_ROOT=true
fi

# ── write systemd unit file ───────────────────────────────────────────────────
UNIT_FILE="/etc/systemd/system/ping-exporter.service"

if [[ "$USE_ROOT" == true ]]; then
  UNIT_USER="root"
else
  UNIT_USER="${SERVICE_USER}"
fi

cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=Ping Exporter — Prometheus ICMP/TCP latency exporter
Documentation=https://github.com/ZhiShengYuan/ping-exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${UNIT_USER}
ExecStart=${BINARY} \\
  --config.url=${CONFIG_URL} \\
  --config.poll-interval=${POLL_INTERVAL} \\
  --web.listen-address=${LISTEN_ADDR} \\
  --ping.privileged=true
Restart=on-failure
RestartSec=5s
# Harden the service even when running as root.
NoNewPrivileges=false
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=

[Install]
WantedBy=multi-user.target
EOF

# NoNewPrivileges must be false when the binary uses file capabilities.
# Re-write it cleanly when running as the dedicated user.
if [[ "$USE_ROOT" == false ]]; then
  sed -i 's/^NoNewPrivileges=false/NoNewPrivileges=true/' "${UNIT_FILE}"
fi

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
echo "  Service user: ${UNIT_USER}"
echo "  Privilege   : $([ "$USE_ROOT" == true ] && echo 'root (setcap unavailable)' || echo 'CAP_NET_RAW via setcap')"
echo ""
echo "  Useful commands:"
echo "    systemctl status ping-exporter"
echo "    journalctl -u ping-exporter -f"
echo "    systemctl restart ping-exporter"
echo ""
