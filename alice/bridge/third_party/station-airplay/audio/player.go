// The shared player grants one session ownership; it never writes global ALSA controls.
package audio

import (
	"context"
	"fmt"
	"github.com/skrashevich/station-airplay/codec"
	"github.com/skrashevich/station-airplay/ptp"
	"log"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Player struct {
	Logger      *log.Logger
	Context     context.Context
	Output      codec.OutputOptions
	Clock       *ptp.VirtualClock
	OutputDelay time.Duration
	BindIP      string
	Timing      *ptp.Server
	mu          sync.Mutex
	owner       *Server
	claimID     uint64
	gain        atomic.Uint64
	// duck is an Alice patch: a local factor under Alice's voice; 0 means none.
	duck   atomic.Uint64
	volume atomic.Uint64
	// SenderVolume and SpeakerVolume are an Alice patch, see VolumeHooks.
	SenderVolume  func(db float64) bool
	SpeakerVolume func() (db float64, ok bool)
}

func NewPlayer(ctx context.Context, clock *ptp.VirtualClock, opts codec.OutputOptions) *Player {
	p := &Player{Logger: log.Default(), Context: ctx, Clock: clock, Output: opts, OutputDelay: 160 * time.Millisecond}
	p.SetVolume(-20)
	return p
}
func (p *Player) claim(s *Server) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner != nil && p.owner != s {
		return fmt.Errorf("another sender owns audio")
	}
	if p.owner != s {
		p.claimID++
		p.owner = s
	}
	if p.Timing != nil {
		p.Timing.Bind(s, []net.IP{s.PeerIP})
	}
	return nil
}
func (p *Player) release(s *Server) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner == s {
		if p.Timing != nil {
			p.Timing.Clear(s)
		}
		p.owner = nil
	}
}
func (p *Player) SetVolume(db float64) {
	p.volume.Store(math.Float64bits(db))
	p.gain.Store(math.Float64bits(codec.LinearVolume(db)))
}
func (p *Player) Gain() float64 {
	gain := math.Float64frombits(p.gain.Load())
	if duck := math.Float64frombits(p.duck.Load()); duck > 0 && duck < 1 {
		gain *= duck
	}
	return gain
}

// SetDuck is an Alice patch: it scales this receiver's PCM by factor (0..1)
// without telling the sender, so the phone's volume slider stays where it is.
// A factor of 1 or more removes the duck.
func (p *Player) SetDuck(factor float64) {
	if !(factor > 0) || math.IsInf(factor, 0) {
		factor = 0.0001
	}
	if factor >= 1 {
		factor = 0
	}
	p.duck.Store(math.Float64bits(factor))
}

func (p *Player) Volume() float64 { return math.Float64frombits(p.volume.Load()) }

// Current returns the sender that owns this player's audio, if any.
func (p *Player) Current() *Server {
	owner, _ := p.Ownership()
	return owner
}

// Ownership returns the audio owner and its claim generation together. Releasing
// audio keeps the generation, so control may survive a partial teardown. A later
// claim advances it even when that sender releases audio before the next query.
func (p *Player) Ownership() (*Server, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owner, p.claimID
}

func (p *Player) PlaybackState() (connected, playing bool) {
	s := p.Current()
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	connected = s.started && s.ctx.Err() == nil
	return connected, connected && s.playing
}

func (p *Player) SetVolumeFor(s *Server, db float64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner != s {
		return false
	}
	p.SetVolume(db)
	return true
}
func (p *Player) SetTimingPeers(s *Server, peers []net.IP, id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner == s && p.Timing != nil {
		p.Timing.SetPeers(s, peers, id)
	}
}

func (p *Player) Logf(format string, args ...any) {
	logger := p.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf(format, args...)
}
