// Package stationairplay embeds an AirPlay audio receiver in a Go application.
// It installs no signal handlers, parses no flags and never changes init, global
// logging, mixer controls or the system clock. The caller owns its lifetime.
package stationairplay

import (
	"context"
	"errors"
	"sync"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
)

var ErrStopping = errors.New("AirPlay receiver is stopping")
var ErrNotRunning = errors.New("AirPlay receiver is not running")
var ErrNoSender = errors.New("no AirPlay sender owns playback")
var ErrControlUnsupported = errors.New("AirPlay control is unsupported by the active sender")

// Status is a snapshot. Running means ready to accept a sender, not playing music.
type Status struct {
	State                State
	Name, Address, Error string
	Connected, Playing   bool
	VolumeDB             float64
}

// The lifecycle owns one protocol service and closes it before permitting reuse.
type service interface {
	Serve(context.Context) error
	Close()
	Status() Status
}

type run struct {
	ctx           context.Context
	cancel        context.CancelFunc
	ready, done   chan struct{}
	startErr, err error
	status        Status
	service       service
}

// Receiver is safe for concurrent Start, Stop, Wait and Status calls.
// Construct it once per speaker and keep the same DataDir across restarts.
type Receiver struct {
	mu      sync.Mutex
	config  settings
	current *run
	state   State
	lastErr error
	open    func(context.Context, settings) (service, error)
}

// New validates configuration only; it neither executes nor requires an installed
// output helper. Check and Start validate the bundled helper before opening PCM.
func New(c Config) (*Receiver, error) {
	cfg, err := normalize(c)
	if err != nil {
		return nil, err
	}
	return &Receiver{config: cfg, state: StateStopped, open: openService}, nil
}

// Start returns after resources and discovery are ready, or after startup rollback.
// It is idempotent while running. The first Start's context owns the service;
// use the application's context, not a short-lived voice/HTTP request context.
func (r *Receiver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if current := r.current; current != nil {
		if r.state == StateStopping {
			r.mu.Unlock()
			return ErrStopping
		}
		r.mu.Unlock()
		select {
		case <-current.ready:
			return current.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	local, cancel := context.WithCancel(ctx)
	current := &run{ctx: local, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	r.current, r.state, r.lastErr = current, StateStarting, nil
	r.mu.Unlock()

	svc, err := r.open(local, r.config)
	if err == nil {
		err = local.Err()
	}
	if err != nil {
		if svc != nil {
			svc.Close()
		}
		cancel()
		r.mu.Lock()
		current.startErr = err
		current.err = err
		if local.Err() == nil || !errors.Is(err, local.Err()) {
			r.lastErr = err
		}
		r.current, r.state = nil, StateStopped
		close(current.ready)
		close(current.done)
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	current.status = svc.Status()
	current.service = svc
	if r.state != StateStopping {
		r.state = StateRunning
	}
	close(current.ready)
	r.mu.Unlock()
	go func() {
		err := svc.Serve(local)
		cancel()
		svc.Close()
		r.mu.Lock()
		current.err = err
		r.lastErr = err
		r.current, r.state = nil, StateStopped
		close(current.done)
		r.mu.Unlock()
	}()
	return nil
}

// Stop requests cancellation and waits until sockets, discovery and audio have
// been released. A deadline only stops waiting; cleanup continues and Start
// returns ErrStopping until that cleanup finishes.
func (r *Receiver) Stop(ctx context.Context) error {
	r.mu.Lock()
	current := r.current
	if current == nil {
		r.mu.Unlock()
		return nil
	}
	r.state = StateStopping
	current.cancel()
	r.mu.Unlock()
	select {
	case <-current.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait observes the current run's completion without stopping it.
func (r *Receiver) Wait(ctx context.Context) error {
	r.mu.Lock()
	current, err := r.current, r.lastErr
	r.mu.Unlock()
	if current == nil {
		return err
	}
	select {
	case <-current.done:
		return current.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Receiver) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{Name: r.config.name}
	if r.current != nil {
		status = r.current.status
		if r.current.service != nil {
			status = r.current.service.Status()
		}
		status.Name = r.config.name
	}
	status.State = r.state
	if r.lastErr != nil {
		status.Error = r.lastErr.Error()
	}
	return status
}
