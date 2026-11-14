package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	airplay "github.com/skrashevich/station-airplay"

	"quasarbridge/internal/nowplaying"
)

const airPlayStartAttempts = 5

type airPlayReceiver interface {
	Start(context.Context) error
	Stop(context.Context) error
	Status() airplay.Status
}

// airPlayControl owns the embedded receiver. The marker file keeps the choice
// across restarts; without it the receiver stays off so that it never takes
// ports from a separately installed AirPlay service.
type airPlayControl struct {
	receiver airPlayReceiver
	app      context.Context
	marker   string
	retry    time.Duration
}

func newAirPlayControl(app context.Context, state, area string) (*airPlayControl, error) {
	if state == "" {
		state = "/data/opt/quasar-bridge"
	}
	name := "Алиса"
	if area != "" {
		name += " " + area
	}
	dir := filepath.Join(state, "airplay")
	receiver, err := airplay.New(airplay.Config{Name: name, DataDir: dir})
	if err != nil {
		return nil, err
	}
	return &airPlayControl{receiver: receiver, app: app, marker: filepath.Join(state, "airplay.enabled"), retry: 20 * time.Second}, nil
}

func (c *airPlayControl) enabled() bool {
	_, err := os.Stat(c.marker)
	return err == nil
}

// resume retries because Wi-Fi may not be ready when the bridge boots.
func (c *airPlayControl) resume() {
	if !c.enabled() {
		return
	}
	for range airPlayStartAttempts {
		err := c.receiver.Start(c.app)
		if err == nil {
			return
		}
		slog.Warn("airplay_start_failed", "error", err)
		select {
		case <-c.app.Done():
			return
		case <-time.After(c.retry):
		}
	}
}

// followAddress restarts the receiver when the speaker's address changes: the
// receiver, its clock and its announcement are bound to the address it started
// with, and phones would keep a dead entry.
func (c *airPlayControl) followAddress(every time.Duration, current func() string) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-c.app.Done():
			return
		case <-ticker.C:
		}
		state := c.receiver.Status()
		if !c.enabled() || state.State != airplay.StateRunning || state.Connected {
			continue
		}
		host, _, err := net.SplitHostPort(state.Address)
		now := current()
		if err != nil || now == "" || host == now {
			continue
		}
		slog.Info("airplay_address_changed", "from", host, "to", now)
		ctx, cancel := context.WithTimeout(c.app, 15*time.Second)
		if err := c.receiver.Stop(ctx); err != nil {
			slog.Warn("airplay_restart_failed", "error", err)
		} else if err := c.receiver.Start(c.app); err != nil {
			slog.Warn("airplay_restart_failed", "error", err)
		}
		cancel()
	}
}

// routeIP is the address the multicast route leaves from, as the receiver picks it.
func routeIP() string {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func (c *airPlayControl) start() error {
	if err := c.receiver.Start(c.app); err != nil {
		return err
	}
	return os.WriteFile(c.marker, nil, 0o600)
}

func (c *airPlayControl) stop(ctx context.Context) error {
	if err := os.Remove(c.marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return c.receiver.Stop(ctx)
}

// shutdown lets the PCM helper close cleanly: a killed ALSA client leaves the
// firmware's dmix locked and every later player silent until a reboot.
func (c *airPlayControl) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.receiver.Stop(ctx); err != nil {
		slog.Warn("airplay_shutdown_failed", "error", err)
	}
}

func (c *airPlayControl) health() map[string]any {
	state := c.receiver.Status()
	return map[string]any{"enabled": c.enabled(), "state": string(state.State), "name": state.Name, "connected": state.Connected, "playing": state.Playing, "error": state.Error}
}

// now gives only the source and status: the receiver reads no track data from the
// sender and does not know the sender's name.
func (c *airPlayControl) now(context.Context) (*nowplaying.Info, error) {
	state := c.receiver.Status()
	switch {
	case state.Playing:
		return &nowplaying.Info{Source: "airplay", Status: nowplaying.Playing}, nil
	case state.Connected:
		return &nowplaying.Info{Source: "airplay", Status: nowplaying.Paused}, nil
	}
	return nil, nil
}

// ServeHTTP handles GET /airplay?action=start|stop|status. It is GET because
// BusyBox wget on Lite 2 has no --post-data; the bridge listens on loopback only.
func (c *airPlayControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var err error
	switch r.URL.Query().Get("action") {
	case "start":
		err = c.start()
	case "stop":
		err = c.stop(ctx)
	case "status", "":
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		slog.Warn("airplay_control_failed", "error", err)
		http.Error(w, "airplay control failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(c.health())
	_, _ = w.Write(data)
}
