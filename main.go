package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	configURL := flag.String("config.url", "", "URL of the remote JSON config (required)")
	pollInterval := flag.Duration("config.poll-interval", 30*time.Second, "How often to poll the remote config")
	listenAddr := flag.String("web.listen-address", ":9427", "Address on which to expose metrics")
	privileged := flag.Bool("ping.privileged", true, "Use privileged raw ICMP sockets (requires root or CAP_NET_RAW)")
	logLevel := flag.String("log.level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	if *configURL == "" {
		fmt.Fprintln(os.Stderr, "error: --config.url is required")
		flag.Usage()
		os.Exit(1)
	}

	// Set up structured logger.
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid log level %q: %v\n", *logLevel, err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// Context cancelled on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start config poller — blocks until first config arrives.
	poller := NewConfigPoller(*configURL, *pollInterval)
	cfgCh := poller.Start(ctx)

	var firstCfg *Config
	select {
	case cfg, ok := <-cfgCh:
		if !ok || cfg == nil {
			slog.Error("failed to load initial config")
			os.Exit(1)
		}
		firstCfg = cfg
	case <-ctx.Done():
		slog.Info("shutting down before initial config loaded")
		return
	}
	slog.Info("initial config loaded", "targets", len(firstCfg.Target))

	// Set up components.
	tm := NewTargetManager(*privileged)
	tm.Reconcile(firstCfg)

	wl := NewIPWhitelist(firstCfg.AllowPrometheusAddress)

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewPingCollector(tm))

	// HTTP server.
	mux := http.NewServeMux()
	mux.Handle("/metrics", wl.Wrap(promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><body><h1>Ping Exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`)
	})

	srv := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
	}

	go func() {
		slog.Info("HTTP server listening", "addr", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "err", err)
			stop()
		}
	}()

	// Main event loop.
	for {
		select {
		case cfg, ok := <-cfgCh:
			if !ok {
				// Channel closed means poller exited (context done).
				goto shutdown
			}
			if cfg == nil {
				continue
			}
			slog.Info("config reloaded", "targets", len(cfg.Target))
			tm.Reconcile(cfg)
			wl.Update(cfg.AllowPrometheusAddress)

		case <-ctx.Done():
			goto shutdown
		}
	}

shutdown:
	slog.Info("shutting down")
	tm.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("HTTP server shutdown error", "err", err)
	}
}
