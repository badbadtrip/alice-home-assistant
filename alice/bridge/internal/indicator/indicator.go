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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Path     string
	mu       sync.Mutex
	active   int
	sources  []func() bool
	reported atomic.Bool
	once     sync.Once
	wake     chan struct{}
	// The firmware draws listening, speaking and the volume level on the same ring.
	// While it does, the bridge stays off the ring instead of fighting it.
	voice    atomic.Value
	volumeAt atomic.Int64
}

// YieldAfterVolume is how long the firmware keeps the ring after a volume change.
const YieldAfterVolume = 1500 * time.Millisecond

func (c *Controller) poke() {
	select {
	case c.wakeup() <- struct{}{}:
	default:
	}
}

// SetVoice receives Alice's state: idle, listening, thinking or speaking.
func (c *Controller) SetVoice(state string) {
	c.voice.Store(state)
	c.poke()
}

// VolumeChanged hands the ring to the firmware's volume display for a moment.
func (c *Controller) VolumeChanged() {
	c.volumeAt.Store(time.Now().UnixNano())
	c.poke()
}

func (c *Controller) yielding(now time.Time) bool {
	if state, _ := c.voice.Load().(string); state != "" && state != "idle" {
		return true
	}
	return now.Sub(time.Unix(0, c.volumeAt.Load())) < YieldAfterVolume
}

func (c *Controller) aliceIdle() bool {
	state, _ := c.voice.Load().(string)
	return state == "" || state == "idle"
}

func (c *Controller) wakeup() chan struct{} {
	c.once.Do(func() { c.wake = make(chan struct{}, 1) })
	return c.wake
}

// AddMusic adds a player that can say for itself whether it is playing.
func (c *Controller) AddMusic(source func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sources = append(c.sources, source)
}

// SetMusic records what Home Assistant knows about the speaker's own player.
func (c *Controller) SetMusic(playing bool) { c.reported.Store(playing) }

func (c *Controller) sourcePlaying() bool {
	c.mu.Lock()
	sources := slices.Clone(c.sources)
	c.mu.Unlock()
	for _, source := range sources {
		if source() {
			return true
		}
	}
	return c.reported.Load()
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
	// A dark ring sleeps between checks; thinking must light it at once.
	select {
	case c.wakeup() <- struct{}{}:
	default:
	}
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

// audioPlaying reads the sound card. A sound server keeps it running in silence,
// so that owner proves nothing; its player reports through AddMusic instead.
func audioPlaying() bool {
	paths, _ := filepath.Glob("/proc/asound/card0/pcm*p/sub0/status")
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(raw, []byte("state: RUNNING")) {
			continue
		}
		var owner int
		for line := range strings.Lines(string(raw)) {
			if value, ok := strings.CutPrefix(line, "owner_pid"); ok {
				owner, _ = strconv.Atoi(strings.TrimSpace(strings.TrimLeft(value, " :\t")))
			}
		}
		command, _ := os.ReadFile("/proc/" + strconv.Itoa(owner) + "/cmdline")
		if !bytes.Contains(command, []byte("pulseaudio")) {
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

// Checked every 300 ms: 1.2 s of sound while Alice is quiet is music, and it stays
// music through a second of silence between tracks.
func (g *musicGate) update(running bool) bool {
	if running {
		g.on, g.off = min(g.on+1, 4), 0
	} else {
		g.off++
		if g.off >= 4 {
			g.on = 0
		}
	}
	return g.on >= 4
}

// Serve draws the ring until ctx ends and leaves it dark.
func (c *Controller) Serve(ctx context.Context) {
	led, err := openRing()
	if err != nil {
		return
	}
	started := time.Now()
	// Frames run at 60 per second only while something shows; a dark ring wakes once a second.
	const frame = 16 * time.Millisecond
	timer := time.NewTimer(0)
	defer timer.Stop()
	var cfg Config
	var gate musicGate
	var nextCheck, loaded time.Time
	var sourced atomic.Bool
	playing, allowed, lit := false, false, false
	black := make([]Pixel, led.size)
	off := make([]Light, led.size)
	var soft smoother
	defer func() {
		if lit {
			_ = led.show(black)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-c.wakeup():
		}
		now := time.Now()
		if now.After(nextCheck) {
			nextCheck = now.Add(300 * time.Millisecond)
			if now.Sub(loaded) > time.Second {
				cfg, allowed, loaded = c.load(), unmuted(), now
			}
			// Alice's own speech also runs the sound card; it never counts as music.
			card := gate.update(audioPlaying() && c.aliceIdle())
			go func() { sourced.Store(c.sourcePlaying()) }()
			playing = card || sourced.Load()
		}
		look, ok := pick(cfg, c.thinking(), playing)
		if c.yielding(now) && !c.thinking() {
			// Leave the last frame alone: the firmware is drawing over it right now.
			lit, ok = false, false
			soft = smoother{}
		}
		switch {
		case ok && allowed:
			_ = led.show(soft.next(look.Render(now.Sub(started), led.size)))
			lit = true
		case lit:
			dim := soft.next(off)
			if soft.dark() {
				dim, lit = black, false
			}
			_ = led.show(dim)
		}
		delay := frame
		if !lit {
			delay = max(frame, time.Until(nextCheck))
		}
		timer.Reset(delay)
	}
}
