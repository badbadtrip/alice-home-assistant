// Package indicator temporarily borrows the ring only during actual model calls.
package indicator

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

//go:embed ring.sh
var script string

//go:embed plum.sh
var plumScript string

type Config struct {
	Enabled    bool   `json:"enabled"`
	Color      string `json:"color"`
	Brightness *int   `json:"brightness,omitempty"`
	Mode       string `json:"mode,omitempty"`
	PeriodMS   int    `json:"period_ms,omitempty"`
}

func (c Config) valid() bool {
	return hex.MatchString(c.Color) && (c.Brightness == nil || (*c.Brightness >= 1 && *c.Brightness <= 100)) &&
		(c.Mode == "" || c.Mode == "steady" || c.Mode == "breathe" || c.Mode == "double" || c.Mode == "rainbow") &&
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
	cancel context.CancelFunc
	done   chan struct{}
}

func New(configPath string) *Controller {
	_, midiErr := os.Stat("/sys/class/leds/cucumber:red:ring23")
	_, plumErr := os.Stat("/sys/class/leds/plum:red:ring3")
	if midiErr == nil || plumErr == nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(configPath), "indicator-ready"), []byte("2\n"), 0600)
	}
	return &Controller{Path: filepath.Join(filepath.Dir(configPath), "indicator.json")}
}

// Begin is shared by every model consumer (answers, classifier and planner).
// A separate shell owns restoration, even if the bridge is killed abruptly.
func (c *Controller) Begin() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active++
	if c.active == 1 {
		var cfg Config
		raw, err := os.ReadFile(c.Path)
		if err == nil && json.Unmarshal(raw, &cfg) == nil && cfg.Enabled && cfg.valid() {
			ctx, cancel := context.WithCancel(context.Background())
			c.cancel, c.done = cancel, make(chan struct{})
			go c.run(ctx, cfg, c.done)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.active--
			if c.active == 0 && c.cancel != nil {
				c.cancel()
				<-c.done
				c.cancel = nil
			}
		})
	}
}

func (c *Controller) run(ctx context.Context, cfg Config, done chan struct{}) {
	defer close(done)
	// Do not use CommandContext: the helper must process EOF and restore triggers.
	helper := script
	_, plumErr := os.Stat("/sys/class/leds/plum:red:ring3")
	plum := plumErr == nil
	if cfg.Mode == "rainbow" && !plum {
		return
	}
	if plum {
		helper = plumScript
	}
	cmd := exec.Command("/bin/sh", "-c", helper, "alice-ring", cfg.Color[1:])
	input, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return
	}
	started := time.Now()
	writeFrame := func(elapsed time.Duration) error {
		if plum {
			f := cfg.Frame(elapsed)
			_, err := fmt.Fprintf(input, "F %d %d %d %d %d %d %d %d %d\n", f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7], f[8])
			return err
		}
		_, err := io.WriteString(input, fmt.Sprintf("%d\n", cfg.Level(elapsed)))
		return err
	}
	_ = writeFrame(0)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	running := true
	for running {
		select {
		case <-ctx.Done():
			running = false
		case <-ticker.C:
			if err := writeFrame(time.Since(started)); err != nil {
				running = false
			}
		}
	}
	input.Close()
	cmd.Wait()
}
