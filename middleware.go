package main

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

// IPWhitelist is an HTTP middleware that restricts access to allowed IP ranges.
type IPWhitelist struct {
	mu      sync.RWMutex
	allowed []*net.IPNet
}

func NewIPWhitelist(addrs []string) *IPWhitelist {
	wl := &IPWhitelist{}
	wl.Update(addrs)
	return wl
}

// Update replaces the allowed list. Thread-safe.
func (wl *IPWhitelist) Update(addrs []string) {
	nets := make([]*net.IPNet, 0, len(addrs))
	for _, a := range addrs {
		var ipnet *net.IPNet
		if strings.Contains(a, "/") {
			_, n, err := net.ParseCIDR(a)
			if err != nil {
				slog.Warn("invalid CIDR in whitelist, skipping", "entry", a, "err", err)
				continue
			}
			ipnet = n
		} else {
			ip := net.ParseIP(a)
			if ip == nil {
				slog.Warn("invalid IP in whitelist, skipping", "entry", a)
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			ipnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		}
		nets = append(nets, ipnet)
	}

	if len(nets) == 0 {
		slog.Warn("WHITELIST IS EMPTY — all Prometheus scrape requests will be denied")
	}

	wl.mu.Lock()
	wl.allowed = nets
	wl.mu.Unlock()
}

// Wrap returns an http.Handler that enforces the IP whitelist.
func (wl *IPWhitelist) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil {
			slog.Warn("could not parse remote IP", "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		wl.mu.RLock()
		allowed := wl.allowed
		wl.mu.RUnlock()

		for _, n := range allowed {
			if n.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}

		slog.Warn("scrape denied by whitelist", "remote", host)
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}
