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
	// Volume is an Alice patch: when set, the sender's slider drives the
	// speaker's own volume instead of this receiver's PCM gain.
	Volume VolumeHooks
}

// VolumeHooks connect the sender's volume slider to the host's volume (Alice
// patch). Sender gets the slider in dB (-30..0, -144 for silence) and reports
// whether the host took it; while it does, the receiver's gain stays at 0 dB
// and only silence (-144) is applied locally. Speaker answers the sender's
// question about the current volume, in the same dB scale.
type VolumeHooks struct {
	Sender  func(db float64) bool
	Speaker func() (db float64, ok bool)
}

type settings struct {
	name, iface, listen, state, aplay, device string
	ownedOutput                               bool
	delay                                     time.Duration
	volume                                    float64
	logger                                    *log.Logger
	hooks                                     VolumeHooks
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
		delay:       160 * time.Millisecond, volume: -20, logger: c.Logger, hooks: c.Volume}
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
