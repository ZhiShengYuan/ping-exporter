package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// TargetStats is a point-in-time snapshot for a single target.
type TargetStats struct {
	Address     string
	Alias       string
	Method      string // "icmp" or "tcp"
	MinRtt      time.Duration
	MaxRtt      time.Duration
	AvgRtt      time.Duration
	StdDevRtt   time.Duration
	PacketLoss  float64 // 0.0–1.0
	PacketsSent int     // total probes attempted
	PacketsRecv int     // successful responses
}

type managedTarget struct {
	cancel context.CancelFunc
	stats  atomic.Pointer[TargetStats]
}

// targetKey returns a unique string key for a TargetConfig.
// TCP targets with the same address but different ports are distinct entries.
func targetKey(tc TargetConfig) string {
	switch tc.Method {
	case "tcp":
		return fmt.Sprintf("tcp:%s:%d", tc.Address, tc.Port)
	default:
		return "icmp:" + tc.Address
	}
}

// normalizeMethod fills in "icmp" when Method is empty.
func normalizeMethod(tc *TargetConfig) {
	if tc.Method == "" {
		tc.Method = "icmp"
	}
}

// TargetManager manages per-target probe goroutines.
type TargetManager struct {
	mu         sync.Mutex
	targets    map[string]*managedTarget
	privileged bool
	interval   time.Duration
}

func NewTargetManager(privileged bool) *TargetManager {
	return &TargetManager{
		targets:    make(map[string]*managedTarget),
		privileged: privileged,
	}
}

// Reconcile diffs current targets against the new config and starts/stops goroutines.
func (tm *TargetManager) Reconcile(cfg *Config) {
	interval := 1 * time.Second
	if cfg.Interval != "" {
		d, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			slog.Warn("invalid interval in config, using 1s", "interval", cfg.Interval, "err", err)
		} else {
			interval = d
		}
	}

	desired := make(map[string]TargetConfig, len(cfg.Target))
	for _, t := range cfg.Target {
		normalizeMethod(&t)
		if t.Method == "tcp" && t.Port <= 0 {
			slog.Warn("tcp target missing port, skipping", "address", t.Address)
			continue
		}
		desired[targetKey(t)] = t
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Stop removed targets.
	for key, mt := range tm.targets {
		if _, ok := desired[key]; !ok {
			slog.Info("removing target", "key", key)
			mt.cancel()
			delete(tm.targets, key)
		}
	}

	intervalChanged := interval != tm.interval
	tm.interval = interval

	for key, tc := range desired {
		if mt, exists := tm.targets[key]; exists {
			if !intervalChanged {
				continue
			}
			slog.Info("restarting target (interval changed)", "key", key)
			mt.cancel()
			delete(tm.targets, key)
			_ = mt
		}
		slog.Info("adding target", "key", key, "alias", tc.Alias)
		ctx, cancel := context.WithCancel(context.Background())
		mt := &managedTarget{cancel: cancel}
		tm.targets[key] = mt
		go tm.runTarget(ctx, tc, interval, mt)
	}
}

func (tm *TargetManager) runTarget(ctx context.Context, tc TargetConfig, interval time.Duration, mt *managedTarget) {
	switch tc.Method {
	case "tcp":
		tm.runTCPTarget(ctx, tc, interval, mt)
	default:
		tm.runICMPTarget(ctx, tc, interval, mt)
	}
}

// runICMPTarget runs an infinite ICMP ping loop using pro-bing.
func (tm *TargetManager) runICMPTarget(ctx context.Context, tc TargetConfig, interval time.Duration, mt *managedTarget) {
	pinger, err := probing.NewPinger(tc.Address)
	if err != nil {
		slog.Error("failed to create ICMP pinger", "address", tc.Address, "err", err)
		return
	}
	pinger.SetPrivileged(tm.privileged)
	pinger.Interval = interval
	pinger.Count = 0 // infinite

	pinger.OnRecv = func(_ *probing.Packet) {
		s := pinger.Statistics()
		snap := &TargetStats{
			Address:     tc.Address,
			Alias:       tc.Alias,
			Method:      "icmp",
			MinRtt:      s.MinRtt,
			MaxRtt:      s.MaxRtt,
			AvgRtt:      s.AvgRtt,
			StdDevRtt:   s.StdDevRtt,
			PacketLoss:  s.PacketLoss / 100.0,
			PacketsSent: s.PacketsSent,
			PacketsRecv: s.PacketsRecv,
		}
		mt.stats.Store(snap)
	}

	if err := pinger.RunWithContext(ctx); err != nil && ctx.Err() == nil {
		slog.Error("ICMP pinger exited unexpectedly", "address", tc.Address, "err", err)
	}
}

// runTCPTarget measures TCP connect latency by dialing on every interval tick.
// Statistics are maintained with Welford's online algorithm for numerically
// stable mean and variance.
func (tm *TargetManager) runTCPTarget(ctx context.Context, tc TargetConfig, interval time.Duration, mt *managedTarget) {
	addr := fmt.Sprintf("%s:%d", tc.Address, tc.Port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	var (
		totalSent int
		totalRecv int
		minRtt    = time.Duration(math.MaxInt64)
		maxRtt    time.Duration
		meanNs    float64 // running mean in nanoseconds
		m2Ns      float64 // running sum of squared deviations
	)

	stddev := func() time.Duration {
		if totalRecv < 2 {
			return 0
		}
		return time.Duration(math.Sqrt(m2Ns / float64(totalRecv-1)))
	}

	probe := func() {
		totalSent++
		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		elapsed := time.Since(start)

		if err != nil {
			if ctx.Err() != nil {
				return // shutting down — don't update stats
			}
			slog.Debug("tcp probe failed", "target", addr, "err", err)
		} else {
			conn.Close()
			totalRecv++
			// Update RTT accumulators.
			if elapsed < minRtt {
				minRtt = elapsed
			}
			if elapsed > maxRtt {
				maxRtt = elapsed
			}
			// Welford's online mean/variance.
			n := float64(totalRecv)
			delta := float64(elapsed) - meanNs
			meanNs += delta / n
			m2Ns += delta * (float64(elapsed) - meanNs)
		}

		rttMin := minRtt
		if totalRecv == 0 {
			rttMin = 0 // avoid emitting MaxInt64 before any success
		}

		snap := &TargetStats{
			Address:     tc.Address,
			Alias:       tc.Alias,
			Method:      "tcp",
			MinRtt:      rttMin,
			MaxRtt:      maxRtt,
			AvgRtt:      time.Duration(meanNs),
			StdDevRtt:   stddev(),
			PacketLoss:  float64(totalSent-totalRecv) / float64(totalSent),
			PacketsSent: totalSent,
			PacketsRecv: totalRecv,
		}
		mt.stats.Store(snap)
	}

	// Probe immediately so we don't wait a full interval for the first sample.
	probe()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

// GetStats returns a snapshot of all current target statistics.
func (tm *TargetManager) GetStats() []TargetStats {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	out := make([]TargetStats, 0, len(tm.targets))
	for _, mt := range tm.targets {
		if s := mt.stats.Load(); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// Stop cancels all running probes.
func (tm *TargetManager) Stop() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for addr, mt := range tm.targets {
		mt.cancel()
		delete(tm.targets, addr)
	}
}
