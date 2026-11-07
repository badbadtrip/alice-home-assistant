package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// LANPort serves the app on the home network, guarded by the Home Functions token.
const LANPort = "18862"

// lanPaths are the routine calls the app makes; installation stays on SSH.
var lanPaths = map[string]bool{"/metrics": true, "/healthz": true, "/volume": true, "/player": true, "/airplay": true, "/music-state": true}

// serveLAN lets the app reach the bridge over HTTP instead of opening SSH for every
// check. The firmware firewall drops unknown ports, so the bridge allows its own.
func serveLAN(ctx context.Context, token string, inner http.Handler) {
	if exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport", LANPort, "-j", "ACCEPT").Run() != nil {
		if err := exec.Command("iptables", "-I", "INPUT", "1", "-p", "tcp", "--dport", LANPort, "-j", "ACCEPT").Run(); err != nil {
			slog.Warn("lan_firewall", "error", err)
		}
	}
	server := &http.Server{Addr: ":" + LANPort, Handler: lanHandler(token, inner), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Warn("lan_unavailable", "error", err)
	}
}

func lanHandler(token string, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || !lanPaths[r.URL.Path] {
			http.Error(w, "not available on the network", http.StatusNotFound)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
