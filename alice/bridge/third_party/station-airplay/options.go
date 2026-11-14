package stationairplay

import (
	"cmp"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"time"
)

// Config configures a receiver without creating state or opening sockets.
// DataDir is required and must be private to this receiver's persistent identity.
// Nil volume and delay select -20 dB and 160 ms; explicit zero values are supported.
// Empty Aplay selects the PCM payload embedded in the host executable. An
// explicit Aplay value selects a custom aplay-compatible command without preflight.
type Config struct {
	Name, Interface, ListenAddress, DataDir, Aplay, Device string
	OutputDelay                                            *time.Duration
	InitialVolumeDB                                        *float64
	Logger                                                 *log.Logger
}

type settings struct {
	name, iface, listen, state, aplay, device string
	ownedOutput                               bool
	delay                                     time.Duration
	volume                                    float64
	logger                                    *log.Logger
}

func normalize(c Config) (settings, error) {
	name := c.Name
	if name == "" {
		host, _ := os.Hostname()
		name = cmp.Or(host, "Station") + " AirPlay"
	}
	s := settings{name: name, iface: c.Interface, listen: cmp.Or(c.ListenAddress, ":7000"),
		state: c.DataDir, aplay: c.Aplay, device: cmp.Or(c.Device, "default"),
		ownedOutput: c.Aplay == "",
		delay:       160 * time.Millisecond, volume: -20, logger: c.Logger}
	if s.ownedOutput {
		executable, err := os.Executable()
		if err != nil {
			return s, fmt.Errorf("locate bundled PCM output: %w", err)
		}
		s.aplay = executable
	}
	if c.OutputDelay != nil {
		s.delay = *c.OutputDelay
	}
	if c.InitialVolumeDB != nil {
		s.volume = *c.InitialVolumeDB
	}
	if s.logger == nil {
		s.logger = log.Default()
	}
	if strings.TrimSpace(s.name) == "" || len(s.name) > 63 || strings.ContainsAny(s.name, "\r\n\x00") {
		return s, fmt.Errorf("AirPlay name must contain 1..63 bytes without control delimiters")
	}
	if strings.TrimSpace(s.state) == "" {
		return s, fmt.Errorf("AirPlay DataDir is required")
	}
	if s.volume < -144 || s.volume > 0 || math.IsNaN(s.volume) || math.IsInf(s.volume, 0) {
		return s, fmt.Errorf("AirPlay volume must be between -144 and 0 dB")
	}
	if s.delay < 0 || s.delay > 5*time.Second {
		return s, fmt.Errorf("AirPlay output delay must be between 0 and 5s")
	}
	if _, _, err := net.SplitHostPort(s.listen); err != nil {
		return s, fmt.Errorf("AirPlay listen address: %w", err)
	}
	return s, nil
}
