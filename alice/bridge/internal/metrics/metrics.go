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
	"sync"
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
	// Rates since the previous reading; nil on the first one.
	CPUPercent  *float64 `json:"cpu_pct"`
	NetRxBps    *float64 `json:"net_rx_bps"`
	NetTxBps    *float64 `json:"net_tx_bps"`
	BridgeRSSKB int64    `json:"bridge_rss_kb"`
}

type counters struct {
	at          time.Time
	busy, total uint64
	rx, tx      uint64
}

var (
	previousMu sync.Mutex
	previous   = map[string]counters{}
)

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
	r.rates(&s)
	if raw, err := os.ReadFile(filepath.Join(r.Proc, "self/status")); err == nil {
		for line := range strings.Lines(string(raw)) {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "VmRSS:" {
				s.BridgeRSSKB, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
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

// rates turns the kernel's running counters into CPU load and network speed.
func (r Root) rates(s *System) {
	now := counters{at: time.Now()}
	if raw, err := os.ReadFile(filepath.Join(r.Proc, "stat")); err == nil {
		line, _, _ := strings.Cut(string(raw), "\n")
		if fields := strings.Fields(line); len(fields) > 4 && fields[0] == "cpu" {
			for i, field := range fields[1:] {
				value, _ := strconv.ParseUint(field, 10, 64)
				now.total += value
				if i != 3 && i != 4 { // idle and iowait
					now.busy += value
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(r.Proc, "net/dev")); err == nil {
		for line := range strings.Lines(string(raw)) {
			name, rest, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "lo" {
				continue
			}
			if fields := strings.Fields(rest); len(fields) > 8 {
				rx, _ := strconv.ParseUint(fields[0], 10, 64)
				tx, _ := strconv.ParseUint(fields[8], 10, 64)
				now.rx, now.tx = now.rx+rx, now.tx+tx
			}
		}
	}
	previousMu.Lock()
	last, ok := previous[r.Proc]
	previous[r.Proc] = now
	previousMu.Unlock()
	if !ok || now.total <= last.total {
		return
	}
	cpu := float64(now.busy-last.busy) / float64(now.total-last.total) * 100
	s.CPUPercent = &cpu
	if seconds := now.at.Sub(last.at).Seconds(); seconds > 0 && now.rx >= last.rx && now.tx >= last.tx {
		rx, tx := float64(now.rx-last.rx)/seconds, float64(now.tx-last.tx)/seconds
		s.NetRxBps, s.NetTxBps = &rx, &tx
	}
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
