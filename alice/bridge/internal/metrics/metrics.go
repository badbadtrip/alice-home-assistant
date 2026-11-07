// Package metrics describes the speaker itself: load, memory, flash, heat and players.
// Every value is read from /proc and /sys on request; nothing here is costly.
package metrics

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"quasarbridge/internal/nowplaying"
)

type System struct {
	UptimeS        int64    `json:"uptime_s"`
	Load1          float64  `json:"load_1m"`
	Load5          float64  `json:"load_5m"`
	CPUs           int      `json:"cpus"`
	MemTotalKB     int64    `json:"mem_total_kb"`
	MemAvailableKB int64    `json:"mem_available_kb"`
	DataTotalKB    int64    `json:"data_total_kb"`
	DataFreeKB     int64    `json:"data_free_kb"`
	TempC          *float64 `json:"temp_c"`
}

type Snapshot struct {
	Status        string                      `json:"status"`
	SpeakerID     string                      `json:"speaker_id"`
	BridgeUptimeS int64                       `json:"bridge_uptime_s"`
	System        System                      `json:"system"`
	NowPlaying    *nowplaying.Info            `json:"now_playing"`
	Players       map[string]*nowplaying.Info `json:"players"`
}

// Root lets tests point the readers at a fake /proc and /sys.
type Root struct {
	Proc string
	Sys  string
	Data string
}

var Device = Root{Proc: "/proc", Sys: "/sys", Data: "/data"}

func (r Root) System() System {
	var s System
	s.CPUs = runtime.NumCPU()
	if raw, err := os.ReadFile(filepath.Join(r.Proc, "uptime")); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			value, _ := strconv.ParseFloat(fields[0], 64)
			s.UptimeS = int64(value)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(r.Proc, "loadavg")); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 1 {
			s.Load1, _ = strconv.ParseFloat(fields[0], 64)
			s.Load5, _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	if file, err := os.Open(filepath.Join(r.Proc, "meminfo")); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				s.MemTotalKB = value
			case "MemAvailable:":
				s.MemAvailableKB = value
			}
		}
		file.Close()
	}
	s.DataTotalKB, s.DataFreeKB = diskKB(r.Data)
	// The hottest zone is what matters for throttling.
	zones, _ := filepath.Glob(filepath.Join(r.Sys, "class/thermal/thermal_zone*/temp"))
	for _, zone := range zones {
		raw, err := os.ReadFile(zone)
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil || milli <= 0 {
			continue
		}
		celsius := milli / 1000
		if s.TempC == nil || celsius > *s.TempC {
			s.TempC = &celsius
		}
	}
	return s
}

// Players asks each source on its own so a quiet player still shows its state.
func Players(ctx context.Context, sources map[string]nowplaying.Source) map[string]*nowplaying.Info {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	result := make(map[string]*nowplaying.Info, len(sources))
	for name, source := range sources {
		info, err := source(ctx)
		if err == nil {
			result[name] = info
		}
	}
	return result
}

// Handler serves the snapshot. With a token it accepts only that bearer token.
func Handler(token string, snapshot func(context.Context) Snapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		if token != "" {
			given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snapshot(r.Context()))
	})
}
