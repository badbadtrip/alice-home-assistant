// Package indicator temporarily borrows the ring only during actual model calls.
package indicator

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled    bool   `json:"enabled"`
	Color      string `json:"color"`
	Brightness *int   `json:"brightness,omitempty"`
	Mode       string `json:"mode,omitempty"`
	PeriodMS   int    `json:"period_ms,omitempty"`
	Music      *Layer `json:"music,omitempty"`
	Lamp       *Layer `json:"lamp,omitempty"`
}

// Layer is a look for the ring outside model calls.
type Layer struct {
	Enabled    bool   `json:"enabled"`
	Color      string `json:"color"`
	Brightness *int   `json:"brightness,omitempty"`
	Mode       string `json:"mode,omitempty"`
	PeriodMS   int    `json:"period_ms,omitempty"`
}

func (l *Layer) look() (Config, bool) {
	if l == nil || !l.Enabled {
		return Config{}, false
	}
	c := Config{Enabled: true, Color: l.Color, Brightness: l.Brightness, Mode: l.Mode, PeriodMS: l.PeriodMS}
	return c, c.valid()
}

func (c Config) valid() bool {
	return hex.MatchString(c.Color) && (c.Brightness == nil || (*c.Brightness >= 1 && *c.Brightness <= 100)) &&
		modes[c.Mode] &&
		(c.PeriodMS == 0 || (c.PeriodMS >= 800 && c.PeriodMS <= 8000))
}

// Level changes only the ring's global dimmer, keeping RGB channels stable.
func (c Config) Level(elapsed time.Duration) int {
	brightness := 100
	if c.Brightness != nil {
		brightness = *c.Brightness
	}
	period := c.PeriodMS
	if period == 0 {
		period = 2400
	}
	phase := math.Mod(float64(elapsed)/float64(time.Millisecond), float64(period)) / float64(period)
	factor := 1.0
	switch c.Mode {
	case "rainbow":
		factor = .875 + .125*math.Cos(2*math.Pi*phase)
	case "breathe":
		factor = .625 + .375*math.Cos(2*math.Pi*phase)
	case "double":
		distance := func(center float64) float64 { d := math.Abs(phase - center); return math.Min(d, 1-d) }
		peak := func(center float64) float64 { d := distance(center) / .065; return math.Exp(-d * d) }
		factor = .2 + .8*math.Max(peak(0), peak(.24))
	}
	return int(math.Round(float64(brightness) * factor))
}

// Frame returns Plum's three RGB zones at the same brightness ceiling as the shell.
func (c Config) Frame(elapsed time.Duration) [9]int {
	var frame [9]int
	period := c.PeriodMS
	if period == 0 {
		period = 2400
	}
	phase := math.Mod(float64(elapsed)/float64(time.Millisecond), float64(period)) / float64(period)
	rgb, _ := strconv.ParseUint(c.Color[1:], 16, 32)
	for zone := 0; zone < 3; zone++ {
		channels := [3]float64{float64(rgb >> 16), float64((rgb >> 8) & 255), float64(rgb & 255)}
		if c.Mode == "rainbow" {
			h := math.Mod(phase+float64(zone)/3, 1) * 6
			x := 1 - math.Abs(math.Mod(h, 2)-1)
			switch int(h) {
			case 0:
				channels = [3]float64{255, 255 * x, 0}
			case 1:
				channels = [3]float64{255 * x, 255, 0}
			case 2:
				channels = [3]float64{0, 255, 255 * x}
			case 3:
				channels = [3]float64{0, 255 * x, 255}
			case 4:
				channels = [3]float64{255 * x, 0, 255}
			default:
				channels = [3]float64{255, 0, 255 * x}
			}
		}
		for channel, value := range channels {
			frame[zone*3+channel] = int(math.Floor(value * 160 * float64(c.Level(elapsed)) / 255 / 100))
		}
	}
	return frame
}

var hex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var modes = map[string]bool{"": true, "steady": true, "breathe": true, "double": true, "rainbow": true, "comet": true, "wave": true, "sparkle": true, "fire": true}

type turnKey struct{}
type turn struct {
	mu     sync.Mutex
	finish func()
	closed bool
}

// WithTurn keeps lazy LED ownership across model/tool/model steps. Purely local
// turns never acquire the ring. Cancellation and completion both release it.
func WithTurn(ctx context.Context) (context.Context, func()) {
	if _, ok := ctx.Value(turnKey{}).(*turn); ok {
		return ctx, func() {}
	}
	state := &turn{}
	finish := func() {
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.closed {
			state.closed = true
			if state.finish != nil {
				state.finish()
			}
		}
	}
	stop := context.AfterFunc(ctx, finish)
	return context.WithValue(ctx, turnKey{}, state), func() { stop(); finish() }
}

func (c *Controller) Request(ctx context.Context) func() {
	if state, ok := ctx.Value(turnKey{}).(*turn); ok {
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.closed && state.finish == nil {
			state.finish = c.Begin()
		}
		return func() {}
	}
	return c.Begin()
}

type Controller struct {
	Path   string
	mu     sync.Mutex
	active int
}

func New(configPath string) *Controller {
	_, midiErr := os.Stat("/sys/class/leds/cucumber:red:ring23")
	_, plumErr := os.Stat("/sys/class/leds/plum:red:ring3")
	if midiErr == nil || plumErr == nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(configPath), "indicator-ready"), []byte("3\n"), 0600)
	}
	return &Controller{Path: filepath.Join(filepath.Dir(configPath), "indicator.json")}
}

// Begin is shared by every model consumer (answers, classifier and planner).
func (c *Controller) Begin() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.active--
		})
	}
}

func (c *Controller) thinking() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active > 0
}

func (c *Controller) load() Config {
	var cfg Config
	raw, err := os.ReadFile(c.Path)
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		return Config{}
	}
	return cfg
}

// pick chooses the look: model call, then music, then lamp.
func pick(cfg Config, thinking, playing bool) (Config, bool) {
	if thinking && cfg.Enabled && cfg.valid() {
		return cfg, true
	}
	if playing {
		if look, ok := cfg.Music.look(); ok {
			return look, true
		}
	}
	return cfg.Lamp.look()
}

// audioPlaying is true for any source: every player ends in the same sound card.
func audioPlaying() bool {
	paths, _ := filepath.Glob("/proc/asound/card0/pcm*p/sub0/status")
	for _, path := range paths {
		if raw, err := os.ReadFile(path); err == nil && bytes.HasPrefix(raw, []byte("state: RUNNING")) {
			return true
		}
	}
	return false
}

func unmuted() bool {
	raw, err := os.ReadFile("/sys/kernel/debug/gpio")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(raw)) {
		if strings.Contains(line, "|mute-switch ") {
			return strings.Contains(line, " in  hi")
		}
	}
	return false
}

// musicGate ignores short sounds: spoken answers must not start the music look.
type musicGate struct{ on, off int }

func (g *musicGate) update(running bool) bool {
	if running {
		g.on, g.off = min(g.on+1, 6), 0
	} else {
		g.off++
		if g.off >= 3 {
			g.on = 0
		}
	}
	return g.on >= 6
}

// Serve draws the ring until ctx ends and leaves it dark.
func (c *Controller) Serve(ctx context.Context) {
	led, err := openRing()
	if err != nil {
		return
	}
	started := time.Now()
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	var cfg Config
	var gate musicGate
	var nextCheck time.Time
	playing, allowed, lit := false, false, false
	dark := make([]Pixel, led.size)
	defer func() {
		if lit {
			_ = led.show(dark)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.After(nextCheck) {
				nextCheck = now.Add(time.Second)
				cfg, allowed = c.load(), unmuted()
				playing = gate.update(audioPlaying())
			}
			look, ok := pick(cfg, c.thinking(), playing)
			if ok && allowed {
				_ = led.show(look.Render(now.Sub(started), led.size))
				lit = true
			} else if lit {
				_ = led.show(dark)
				lit = false
			}
		}
	}
}
