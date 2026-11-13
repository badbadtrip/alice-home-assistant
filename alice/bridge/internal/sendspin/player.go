// Package sendspin plays Music Assistant on this speaker.
//
// The app holds the Sendspin session with Music Assistant and relays raw PCM over
// one long-lived request (GET /sendspin/stream). This package reads that stream,
// applies Music Assistant's volume and ducks under Alice, and writes the samples
// to an output, normally the speaker's own ALSA helper.
package sendspin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Output is one open playback. Write blocks at the playback rate; Close stops
// output promptly and releases the device.
type Output interface {
	io.Writer
	Close() error
}

// Opener starts playback of interleaved S16LE PCM.
type Opener func(ctx context.Context, rate, channels int) (Output, error)

const (
	// The app sends a keepalive every 5 s; silence for this long means a dead link.
	idleLimit = 20 * time.Second
	maxFrame  = 1 << 20
	// Alice's voice must stay clear over music: this is the music's level while she listens or speaks.
	duckGain = 0.15
)

var retry = []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second}

// Player is the speaker's Music Assistant player.
type Player struct {
	URL, Token, Speaker string
	HTTP                *http.Client
	Open                Opener
	// OnChange is told when playback starts or stops, for the speaker's state report.
	OnChange func()

	ducked    atomic.Bool
	volume    atomic.Int32
	muted     atomic.Bool
	streaming atomic.Bool
	linked    atomic.Bool
}

// SetAliceState ducks the music while Alice listens, thinks or speaks.
func (p *Player) SetAliceState(state string) { p.ducked.Store(state != "" && state != "idle") }

// Streaming reports whether Music Assistant is playing here now.
func (p *Player) Streaming() bool { return p.streaming.Load() }

// Linked reports whether the stream from the app is open.
func (p *Player) Linked() bool { return p.linked.Load() }

// Run keeps the stream open until ctx ends.
func (p *Player) Run(ctx context.Context) {
	p.volume.Store(100)
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := p.session(ctx)
		p.linked.Store(false)
		p.setStreaming(false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			failures = 0
		}
		wait := retry[min(failures, len(retry)-1)]
		if failures == 0 || failures == len(retry) {
			slog.Info("sendspin_reconnect", "error", errorClass(err), "wait_s", int(wait.Seconds()))
		}
		failures++
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func errorClass(err error) string {
	switch {
	case err == nil:
		return "closed"
	case errors.Is(err, errIdle):
		return "idle"
	case errors.Is(err, errStatus):
		return err.Error()
	}
	return "network"
}

var (
	errIdle   = errors.New("stream idle")
	errStatus = errors.New("http")
)

func (p *Player) session(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/sendspin/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-Alice-Speaker", p.Speaker)
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w %d", errStatus, resp.StatusCode)
	}
	p.linked.Store(true)
	// Any frame resets the watchdog; the app sends keepalives while idle.
	alive := make(chan struct{}, 1)
	var idle atomic.Bool
	go func() {
		timer := time.NewTimer(idleLimit)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-alive:
				timer.Reset(idleLimit)
			case <-timer.C:
				idle.Store(true)
				cancel()
				return
			}
		}
	}()
	s := &stream{player: p, ctx: ctx}
	defer s.closing.Wait()
	defer s.stop()
	err = s.read(bufio.NewReaderSize(resp.Body, 64<<10), func() {
		select {
		case alive <- struct{}{}:
		default:
		}
	})
	if idle.Load() {
		return errIdle
	}
	return err
}

func (p *Player) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p *Player) setStreaming(on bool) {
	if p.streaming.Swap(on) != on && p.OnChange != nil {
		p.OnChange()
	}
}

// Control sends a playback command (pause, play, next, previous, stop) to Music Assistant.
func (p *Player) Control(ctx context.Context, action string) error {
	body, _ := json.Marshal(map[string]string{"action": action})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+"/sendspin/control", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-Alice-Speaker", p.Speaker)
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w %d", errStatus, resp.StatusCode)
	}
	return nil
}

// stream is one open request: the current format and the open output.
type stream struct {
	player         *Player
	ctx            context.Context
	rate, channels int
	out            Output
	gain           float64
	openFailed     time.Time
	mu             sync.Mutex
	// closing counts helpers stopped in the background after a skip.
	closing sync.WaitGroup
}

func (s *stream) read(r *bufio.Reader, alive func()) error {
	var header [5]byte
	for {
		kind, err := r.ReadByte()
		if err != nil {
			return err
		}
		alive()
		switch kind {
		case 'K':
		case 'F':
			if _, err := io.ReadFull(r, header[:5]); err != nil {
				return err
			}
			rate, channels := int(binary.BigEndian.Uint32(header[:4])), int(header[4])
			if rate != 44100 && rate != 48000 || channels != 2 {
				return fmt.Errorf("unsupported format %d/%d", rate, channels)
			}
			if rate != s.rate || channels != s.channels {
				s.stop()
				s.rate, s.channels = rate, channels
			}
			s.player.setStreaming(true)
		case 'V':
			if _, err := io.ReadFull(r, header[:2]); err != nil {
				return err
			}
			s.player.volume.Store(int32(min(header[0], 100)))
			s.player.muted.Store(header[1] != 0)
		case 'C':
			// A skip: the old helper is silenced in the background and the next track
			// opens a new one at once. Waiting for the old one to exit held the next
			// track back; ALSA's dmix takes both for that moment.
			s.release()
		case 'E':
			s.release()
			s.player.setStreaming(false)
		case 'A':
			if _, err := io.ReadFull(r, header[:4]); err != nil {
				return err
			}
			size := binary.BigEndian.Uint32(header[:4])
			if size > maxFrame || size%4 != 0 {
				return fmt.Errorf("invalid audio frame %d", size)
			}
			pcm := make([]byte, size)
			if _, err := io.ReadFull(r, pcm); err != nil {
				return err
			}
			s.play(pcm)
		default:
			return fmt.Errorf("unknown frame %q", kind)
		}
	}
}

func (s *stream) play(pcm []byte) {
	if s.rate == 0 {
		return // audio before a format is a protocol error on the app side; drop it
	}
	if s.out == nil {
		// A broken helper must not be restarted for every 25 ms frame.
		if !s.openFailed.IsZero() && time.Since(s.openFailed) < 3*time.Second {
			return
		}
		out, err := s.player.Open(s.ctx, s.rate, s.channels)
		if err != nil {
			s.openFailed = time.Now()
			slog.Warn("sendspin_output_failed", "error", err)
			return
		}
		s.openFailed = time.Time{}
		s.mu.Lock()
		s.out = out
		s.mu.Unlock()
		s.gain = s.target() // start at the right level, no fade from silence or full
	}
	s.gain = applyGain(pcm, s.gain, s.target())
	if _, err := s.out.Write(pcm); err != nil {
		slog.Warn("sendspin_output_write_failed", "error", err)
		s.stop()
	}
}

func (s *stream) target() float64 {
	p := s.player
	if p.muted.Load() {
		return 0
	}
	g := math.Pow(float64(p.volume.Load())/100, 1.5)
	if p.ducked.Load() {
		g *= duckGain
	}
	return g
}

// release detaches the output and closes it without waiting.
func (s *stream) release() {
	s.mu.Lock()
	out := s.out
	s.out = nil
	s.mu.Unlock()
	if out == nil {
		return
	}
	s.closing.Go(func() {
		if err := out.Close(); err != nil {
			slog.Warn("sendspin_output_close_failed", "error", err)
		}
	})
}

func (s *stream) stop() {
	s.mu.Lock()
	out := s.out
	s.out = nil
	s.mu.Unlock()
	if out != nil {
		if err := out.Close(); err != nil {
			slog.Warn("sendspin_output_close_failed", "error", err)
		}
	}
}

// applyGain scales S16LE samples, ramping from one gain to the next across the
// buffer so that a change of volume or ducking makes no click. It returns the
// gain reached at the end.
func applyGain(pcm []byte, from, to float64) float64 {
	frames := len(pcm) / 4
	if frames == 0 {
		return from
	}
	if from == 1 && to == 1 {
		return 1
	}
	// Ducking should be quick but not abrupt: at most a full swing over ~60 ms.
	const maxStepPerFrame = 1.0 / (0.06 * 48000)
	gain := from
	for i := 0; i < frames; i++ {
		switch {
		case gain < to:
			gain = math.Min(to, gain+maxStepPerFrame)
		case gain > to:
			gain = math.Max(to, gain-maxStepPerFrame)
		}
		for c := 0; c < 2; c++ {
			o := i*4 + c*2
			v := float64(int16(binary.LittleEndian.Uint16(pcm[o:]))) * gain
			binary.LittleEndian.PutUint16(pcm[o:], uint16(int16(math.Max(-32768, math.Min(32767, math.Round(v))))))
		}
	}
	return gain
}

// Play asks Music Assistant to find and play music here. kind is track, artist,
// album, playlist or any; an empty query resumes what was playing.
func (p *Player) Play(ctx context.Context, kind, query string) error {
	body, _ := json.Marshal(map[string]string{"kind": kind, "query": query})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+"/sendspin/play", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-Alice-Speaker", p.Speaker)
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	}
	return fmt.Errorf("%w %d", errStatus, resp.StatusCode)
}

// ErrNotFound means Music Assistant found nothing to play.
var ErrNotFound = errors.New("nothing found")
