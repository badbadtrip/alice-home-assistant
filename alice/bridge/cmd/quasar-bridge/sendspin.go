package main

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	airplay "github.com/skrashevich/station-airplay"

	"quasarbridge/internal/nowplaying"
	"quasarbridge/internal/sendspin"
)

// pcmHelper finds the ALSA helper that the AirPlay receiver ships inside this
// binary. Resolving it through the receiver validates and unpacks it once.
type pcmHelper struct {
	receiver interface {
		Check(context.Context) (airplay.CheckResult, error)
	}
	mu      sync.Mutex
	command string
	env     []string
}

func newPCMHelper(control *airPlayControl) (*pcmHelper, error) {
	if control == nil {
		return nil, errors.New("audio helper is unavailable")
	}
	checker, ok := control.receiver.(interface {
		Check(context.Context) (airplay.CheckResult, error)
	})
	if !ok {
		return nil, errors.New("audio helper is unavailable")
	}
	return &pcmHelper{receiver: checker}, nil
}

func (h *pcmHelper) resolve(ctx context.Context) (string, []string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.command != "" {
		if _, err := os.Stat(h.command); err == nil {
			return h.command, h.env, nil
		}
	}
	result, err := h.receiver.Check(ctx)
	if err != nil {
		return "", nil, err
	}
	env, err := helperEnvironment(result.Aplay)
	if err != nil {
		return "", nil, err
	}
	h.command, h.env = result.Aplay, env
	return h.command, h.env, nil
}

// helperEnvironment mirrors station-airplay's choice of the native ALSA library
// (Apache-2.0, see NOTICE): the first libasound.so.2 built for the same ABI as
// the helper, with its directory first on the loader path.
func helperEnvironment(helper string) ([]string, error) {
	want, err := elfMachine(helper)
	if err != nil {
		return nil, err
	}
	dirs := filepath.SplitList(os.Getenv("LD_LIBRARY_PATH"))
	dirs = append(dirs, "/system/vendor/quasar/libs", "/lib", "/usr/lib", "/lib/arm-linux-gnueabihf",
		"/usr/lib/arm-linux-gnueabihf", "/lib/aarch64-linux-gnu", "/usr/lib/aarch64-linux-gnu", "/opt/lib")
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		library := filepath.Join(dir, "libasound.so.2")
		if got, err := elfMachine(library); err != nil || got != want {
			continue
		}
		search := dir
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			search += string(os.PathListSeparator) + existing
		}
		env := []string{"STATION_PCM_LIBRARY=" + library, "LD_LIBRARY_PATH=" + search}
		_, pathSet := os.LookupEnv("ALSA_CONFIG_PATH")
		_, dirSet := os.LookupEnv("ALSA_CONFIG_DIR")
		if !pathSet && !dirSet {
			const config = "/system/workdir/lib/alsa/alsa.conf"
			if info, err := os.Stat(config); err == nil && info.Mode().IsRegular() {
				env = append(env, "ALSA_CONFIG_PATH="+config, "ALSA_CONFIG_DIR="+filepath.Dir(config))
			}
		}
		return env, nil
	}
	return nil, errors.New("no ALSA library matches the audio helper")
}

func elfMachine(path string) (elf.Machine, error) {
	file, err := elf.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return file.Machine, nil
}

// Open starts the helper for one playback.
func (h *pcmHelper) Open(ctx context.Context, rate, channels int) (sendspin.Output, error) {
	command, env, err := h.resolve(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(command, "-D", "default", "-r", strconv.Itoa(rate), "-f", "S16_LE", "-c", strconv.Itoa(channels), "-t", "raw", "-q")
	cmd.Env = append(os.Environ(), env...)
	stderr := &tail{left: 2048}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out := &helperOutput{cmd: cmd, stdin: stdin, stderr: stderr, done: make(chan struct{})}
	go func() { out.err = cmd.Wait(); close(out.done) }()
	return out, nil
}

type helperOutput struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail
	done   chan struct{}
	err    error
	once   sync.Once
}

func (o *helperOutput) Write(p []byte) (int, error) {
	n, err := o.stdin.Write(p)
	if err != nil {
		select {
		case <-o.done:
			return n, fmt.Errorf("audio helper exited: %s", strings.TrimSpace(o.stderr.String()))
		default:
		}
	}
	return n, err
}

// Close stops at once. SIGTERM lets the helper close ALSA itself: a killed
// client leaves the firmware's dmix locked and every later player silent.
func (o *helperOutput) Close() error {
	o.once.Do(func() {
		_ = o.cmd.Process.Signal(syscall.SIGTERM)
		_ = o.stdin.Close()
		select {
		case <-o.done:
		case <-time.After(3 * time.Second):
			_ = o.cmd.Process.Kill()
			<-o.done
		}
	})
	return nil
}

// tail keeps the start of the helper's error output; the helper writes it from
// another goroutine than the one that reports a failed write.
type tail struct {
	mu   sync.Mutex
	buf  []byte
	left int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := min(len(p), t.left)
	t.buf = append(t.buf, p[:n]...)
	t.left -= n
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// sendspinNow shows Music Assistant on the speaker card while it plays.
func sendspinNow(player *sendspin.Player) nowplaying.Source {
	return func(context.Context) (*nowplaying.Info, error) {
		if player == nil || !player.Streaming() {
			return nil, nil
		}
		return &nowplaying.Info{Source: "music_assistant", Status: nowplaying.Playing}, nil
	}
}
