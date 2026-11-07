package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"time"

	"quasarbridge/internal/metrics"
)

// MetricsPort serves /metrics to the home network, guarded by the Home Functions token.
const MetricsPort = "18862"

// serveMetrics lets the app read the speaker over HTTP instead of opening SSH every few
// seconds. The firmware firewall drops unknown ports, so the bridge allows its own.
func serveMetrics(ctx context.Context, token string, snapshot func(context.Context) metrics.Snapshot) {
	if exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport", MetricsPort, "-j", "ACCEPT").Run() != nil {
		if err := exec.Command("iptables", "-I", "INPUT", "1", "-p", "tcp", "--dport", MetricsPort, "-j", "ACCEPT").Run(); err != nil {
			slog.Warn("metrics_firewall", "error", err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler(token, snapshot))
	server := &http.Server{Addr: ":" + MetricsPort, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Warn("metrics_unavailable", "error", err)
	}
}
