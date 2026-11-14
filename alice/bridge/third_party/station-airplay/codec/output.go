package codec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type OutputOptions struct {
	Command string
	Args    []string
	Env     []string
	Device  string
	Rate    int
}
type Output struct {
	input  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	err    error // published by closing done
	stderr tailWriter
	once   sync.Once
}
type tailWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.b = append(w.b, p...)
	if len(w.b) > 4096 {
		w.b = append([]byte(nil), w.b[len(w.b)-4096:]...)
	}
	return n, nil
}
func (w *tailWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return string(w.b) }

func OpenOutput(ctx context.Context, opts OutputOptions) (*Output, error) {
	if opts.Rate != 44100 && opts.Rate != 48000 {
		return nil, fmt.Errorf("unsupported output rate %d", opts.Rate)
	}
	if opts.Command == "" {
		opts.Command = "aplay"
	}
	if opts.Device == "" {
		opts.Device = "default"
	}
	ctx, cancel := context.WithCancel(ctx)
	args := append([]string(nil), opts.Args...)
	args = append(args, "-q", "-D", opts.Device, "-t", "raw", "-f", "S16_LE", "-c", "2", "-r", strconv.Itoa(opts.Rate))
	cmd := exec.CommandContext(ctx, opts.Command, args...)
	cmd.Env = append(os.Environ(), opts.Env...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	o := &Output{cancel: cancel, done: make(chan struct{})}
	cmd.Stderr = &o.stderr
	var err error
	o.input, err = cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		o.input.Close()
		cancel()
		return nil, fmt.Errorf("start aplay: %w", err)
	}
	go func() { o.err = cmd.Wait(); close(o.done) }()
	return o, nil
}
func (o *Output) Write(p []byte) (int, error) {
	select {
	case <-o.done:
		return 0, fmt.Errorf("aplay exited: %v: %s", o.err, o.stderr.String())
	default:
	}
	return o.input.Write(p)
}

// Drain finishes a finite diagnostic stream; playback stop/flush uses Close instead.
func (o *Output) Drain(ctx context.Context) error {
	o.input.Close()
	select {
	case <-o.done:
		if o.err != nil {
			return fmt.Errorf("aplay: %w: %s", o.err, o.stderr.String())
		}
		return nil
	case <-ctx.Done():
		o.Close()
		return ctx.Err()
	}
}
func (o *Output) Close() error {
	// Signal Stop before exposing EOF: EOF asks ALSA to drain, while Stop must
	// drop the buffer. The child closes its read end on exit, unblocking writers.
	o.once.Do(o.cancel)
	<-o.done
	o.input.Close()
	return nil
}
