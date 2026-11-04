// Package indicator temporarily borrows the ring only during actual model calls.
package indicator

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

//go:embed ring.sh
var script string

type Config struct {
	Enabled bool   `json:"enabled"`
	Color   string `json:"color"`
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
	if _, err := os.Stat("/sys/class/leds/cucumber:red:ring23"); err == nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(configPath), "indicator-ready"), []byte("1\n"), 0600)
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
		if err == nil && json.Unmarshal(raw, &cfg) == nil && cfg.Enabled && hex.MatchString(cfg.Color) {
			ctx, cancel := context.WithCancel(context.Background())
			c.cancel, c.done = cancel, make(chan struct{})
			go c.run(ctx, cfg.Color, c.done)
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

func (c *Controller) run(ctx context.Context, color string, done chan struct{}) {
	defer close(done)
	// Do not use CommandContext: the helper must process EOF and restore triggers.
	cmd := exec.Command("/bin/sh", "-c", script, "alice-ring", color[1:])
	input, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	running := true
	for running {
		select {
		case <-ctx.Done():
			running = false
		case <-ticker.C:
			if _, err := io.WriteString(input, "tick\n"); err != nil {
				running = false
			}
		}
	}
	input.Close()
	cmd.Wait()
}
