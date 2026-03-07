package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Labels: target, alias, method
	descRttBest   = prometheus.NewDesc("ping_rtt_best_seconds", "Best round-trip time", []string{"target", "alias", "method"}, nil)
	descRttWorst  = prometheus.NewDesc("ping_rtt_worst_seconds", "Worst round-trip time", []string{"target", "alias", "method"}, nil)
	descRttMean   = prometheus.NewDesc("ping_rtt_mean_seconds", "Mean round-trip time", []string{"target", "alias", "method"}, nil)
	descRttStdDev = prometheus.NewDesc("ping_rtt_std_dev_seconds", "Std-dev of round-trip time", []string{"target", "alias", "method"}, nil)
	descLoss      = prometheus.NewDesc("ping_loss_ratio", "Packet loss / connection-failure ratio (0.0–1.0)", []string{"target", "alias", "method"}, nil)
	descUp        = prometheus.NewDesc("ping_up", "Exporter health (always 1)", nil, nil)
)

// PingCollector implements prometheus.Collector.
type PingCollector struct {
	tm *TargetManager
}

func NewPingCollector(tm *TargetManager) *PingCollector {
	return &PingCollector{tm: tm}
}

func (c *PingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRttBest
	ch <- descRttWorst
	ch <- descRttMean
	ch <- descRttStdDev
	ch <- descLoss
	ch <- descUp
}

func (c *PingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(descUp, prometheus.GaugeValue, 1)

	for _, s := range c.tm.GetStats() {
		if s.PacketsSent == 0 {
			continue
		}
		labels := []string{s.Address, s.Alias, s.Method}

		// Loss is meaningful as soon as any probe has been sent.
		ch <- prometheus.MustNewConstMetric(descLoss, prometheus.GaugeValue, s.PacketLoss, labels...)

		// RTT metrics require at least one successful response.
		if s.PacketsRecv == 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(descRttBest, prometheus.GaugeValue, s.MinRtt.Seconds(), labels...)
		ch <- prometheus.MustNewConstMetric(descRttWorst, prometheus.GaugeValue, s.MaxRtt.Seconds(), labels...)
		ch <- prometheus.MustNewConstMetric(descRttMean, prometheus.GaugeValue, s.AvgRtt.Seconds(), labels...)
		ch <- prometheus.MustNewConstMetric(descRttStdDev, prometheus.GaugeValue, s.StdDevRtt.Seconds(), labels...)
	}
}
