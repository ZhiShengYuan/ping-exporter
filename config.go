package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type TargetConfig struct {
	Address string `json:"address"`
	Alias   string `json:"alias"`
	// Method is "icmp" (default) or "tcp".
	Method string `json:"method"`
	// Port is required when Method is "tcp".
	Port int `json:"port"`
}

type Config struct {
	Version                string         `json:"version"`
	Target                 []TargetConfig `json:"target"`
	Interval               string         `json:"interval"`
	AllowPrometheusAddress []string       `json:"allow_prometheus_address"`
}

type ConfigPoller struct {
	url          string
	pollInterval time.Duration
	client       *http.Client
	etag         string
	lastModified string
}

func NewConfigPoller(url string, pollInterval time.Duration) *ConfigPoller {
	return &ConfigPoller{
		url:          url,
		pollInterval: pollInterval,
		client:       &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *ConfigPoller) fetch() (*Config, error) {
	req, err := http.NewRequest(http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if p.etag != "" {
		req.Header.Set("If-None-Match", p.etag)
	}
	if p.lastModified != "" {
		req.Header.Set("If-Modified-Since", p.lastModified)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		p.etag = etag
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		p.lastModified = lm
	}

	return &cfg, nil
}

// Start fetches the config immediately, sends on the returned channel, then
// polls on the given interval. The channel is buffered (size 1) so the poller
// never blocks on a slow consumer.
func (p *ConfigPoller) Start(ctx context.Context) <-chan *Config {
	ch := make(chan *Config, 1)
	go func() {
		defer close(ch)

		sendConfig := func() {
			cfg, err := p.fetch()
			if err != nil {
				slog.Warn("config fetch failed", "err", err)
				return
			}
			if cfg == nil {
				slog.Debug("config not modified")
				return
			}
			select {
			case ch <- cfg:
			default:
				// drop if consumer hasn't read previous config yet
			}
		}

		// Fetch immediately so main can block on first config.
		sendConfig()

		ticker := time.NewTicker(p.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendConfig()
			}
		}
	}()
	return ch
}
