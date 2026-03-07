# ping-exporter

A Prometheus exporter that continuously pings a list of targets (ICMP or TCP) and exposes RTT and packet-loss metrics. The target list is fetched from a remote JSON URL and hot-reloaded without restart.

## Features

- **ICMP and TCP probes** — monitor reachability at both layers
- **Remote config** with ETag/If-Modified-Since caching — zero-downtime reloads
- **IP allowlist** on `/metrics` — scrape access restricted to declared CIDRs
- **Tailscale-aware** — ships with `discover.py` to auto-populate Prometheus `file_sd_configs` from your Tailscale mesh
- **No root required at runtime** — systemd `DynamicUser` + `AmbientCapabilities=CAP_NET_RAW`

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `ping_rtt_best_seconds` | gauge | Best (minimum) RTT observed |
| `ping_rtt_worst_seconds` | gauge | Worst (maximum) RTT observed |
| `ping_rtt_mean_seconds` | gauge | Mean RTT (Welford online algorithm) |
| `ping_rtt_std_dev_seconds` | gauge | RTT standard deviation |
| `ping_loss_ratio` | gauge | Packet/connection loss ratio (0–1) |
| `ping_up` | gauge | Always 1 — exporter health check |

All metrics carry labels: `target` (IP), `alias` (friendly name), `method` (`icmp` or `tcp`).

RTT metrics are only emitted after at least one reply is received. Loss is only emitted after at least one probe has been sent.

## Config

The exporter polls a JSON URL for its configuration:

```json
{
  "version": "1",
  "interval": "1s",
  "target": [
    {"address": "8.8.8.8",   "alias": "google-dns"},
    {"address": "1.1.1.1",   "alias": "cloudflare-dns"},
    {"address": "8.8.8.8",   "alias": "google-dns-tcp", "method": "tcp", "port": 53}
  ],
  "allow_prometheus_address": ["127.0.0.1", "10.0.0.0/8"]
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `version` | — | Config schema version, must be `"1"` |
| `interval` | — | Probe interval (Go duration string, e.g. `"1s"`, `"500ms"`) |
| `target[].address` | — | IP or hostname to probe |
| `target[].alias` | — | Human-readable label used in metrics and dashboards |
| `target[].method` | `"icmp"` | `"icmp"` or `"tcp"` |
| `target[].port` | — | Required when `method` is `"tcp"` |
| `allow_prometheus_address` | — | CIDR list allowed to scrape `/metrics`; empty = deny all |

## Flags

```
--config.url <url>              Remote config URL (required)
--config.poll-interval <dur>    Poll interval for remote config (default: 30s)
--web.listen-address <addr>     Metrics listen address (default: :9427)
--ping.privileged <bool>        Use raw ICMP sockets; requires CAP_NET_RAW (default: true)
--log.level <level>             debug | info | warn | error (default: info)
```

## Building

```bash
go build -o ping-exporter .
```

Pre-built binaries for `linux/amd64` and `linux/arm64` are attached to each GitHub release.

## Installation (systemd)

`setup.sh` downloads the appropriate binary and installs a hardened systemd service unit. No system user is created — systemd `DynamicUser` allocates a transient UID, and `AmbientCapabilities=CAP_NET_RAW` grants raw socket access without `setcap` or running as root.

Requires systemd ≥ 232 (Debian 9+, Ubuntu 16.04+).

```bash
sudo ./setup.sh \
  --bin-url    https://github.com/ZhiShengYuan/ping-exporter/releases/download/v1.0.0 \
  --config-url http://my-config-server/ping-config.json
```

Optional flags:

```
--listen-addr   <addr>   HTTP listen address (default: :9427)
--poll-interval <dur>    Config poll interval (default: 30s)
--install-dir   <dir>    Binary install directory (default: /usr/local/bin)
```

Once installed:

```bash
systemctl status ping-exporter
journalctl -u ping-exporter -f
```

## Tailscale Discovery (`discover.py`)

`discover.py` scans your Tailscale mesh for nodes that have ping-exporter running and writes a Prometheus [`file_sd_configs`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#file_sd_config) JSON file. Each entry carries a `hostname` label (the Tailscale hostname) so dashboards can display friendly names instead of raw IPs.

```bash
# One-shot, print to stdout
python3 discover.py

# One-shot, write to file
python3 discover.py -o /etc/prometheus/ping_sd.json

# Daemon mode: rescan every 60 s and atomically update the file
python3 discover.py -o /etc/prometheus/ping_sd.json --interval 60
```

Prometheus config snippet:

```yaml
scrape_configs:
  - job_name: ping
    file_sd_configs:
      - files: [/etc/prometheus/ping_sd.json]
        refresh_interval: 1m
```

## Grafana Dashboard

Import `grafana-dashboard.json` into Grafana (Dashboard → Import → Upload JSON).

The dashboard has two rows:

| Row | Variable | Shows |
|-----|----------|-------|
| **By Host** | `$hostname` (Tailscale hostname) | All destinations probed from one host |
| **By Destination** | `$alias` | All hosts' latency to one destination |

Both variables auto-populate on dashboard load. The `hostname` label is populated by `discover.py` via Prometheus `file_sd_configs`.
