package stationairplay

import (
	"context"
	"fmt"
	"github.com/skrashevich/station-airplay/audio"
	"math"
)

// A phone may tear down only its audio stream after a remote pause. Keep
// control of that explicitly paused sender while its event channel is alive.
type pausedRemote struct {
	owner     *audio.Server
	claimID   uint64
	send      func(context.Context, string) error
	available func() bool
}

func (s *protocolService) pausedSender() *pausedRemote {
	s.pausedMu.Lock()
	defer s.pausedMu.Unlock()
	p := s.pausedRemote
	if p != nil {
		owner, claimID := s.player.Ownership()
		if claimID != p.claimID || (owner != nil && owner != p.owner) || !p.available() {
			s.pausedRemote = nil
			return nil
		}
	}
	return p
}

// Control sends a media command to the current sender, without disabling the
// receiver. Source commands require a working encrypted event channel.
func (r *Receiver) Control(ctx context.Context, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	current := r.current
	if current == nil || r.state != StateRunning {
		r.mu.Unlock()
		return ErrNotRunning
	}
	control, ok := current.service.(interface {
		Control(context.Context, string) error
	})
	r.mu.Unlock()
	if !ok {
		return ErrControlUnsupported
	}
	return control.Control(ctx, action)
}

// SetVolume changes only this receiver's PCM gain in dB (-144 mutes, 0 max).
func (r *Receiver) SetVolume(ctx context.Context, db float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validVolume(db) {
		return fmt.Errorf("volume must be between -144 and 0 dB")
	}
	r.mu.Lock()
	current := r.current
	if current == nil || r.state != StateRunning {
		r.mu.Unlock()
		return ErrNotRunning
	}
	control, ok := current.service.(interface {
		SetVolume(context.Context, float64) error
	})
	r.mu.Unlock()
	if !ok {
		return ErrControlUnsupported
	}
	return control.SetVolume(ctx, db)
}

// SetDuck is an Alice patch: it lowers only this receiver's output by a local
// factor (0..1, 1 restores), applied right before output, after the AirPlay
// buffer, and never reported to the sender.
func (r *Receiver) SetDuck(factor float64) error {
	r.mu.Lock()
	current := r.current
	if current == nil || r.state != StateRunning {
		r.mu.Unlock()
		return ErrNotRunning
	}
	duck, ok := current.service.(interface{ SetDuck(float64) })
	r.mu.Unlock()
	if !ok {
		return ErrControlUnsupported
	}
	duck.SetDuck(factor)
	return nil
}

func (s *protocolService) SetDuck(factor float64) { s.player.SetDuck(factor) }

// ReportVolume is an Alice patch: it moves the sender's volume slider to db
// (-30..0, -144 silence) without touching this receiver's gain, so that the
// phone shows a volume the speaker itself changed.
func (r *Receiver) ReportVolume(ctx context.Context, db float64) error {
	if !validVolume(db) {
		return fmt.Errorf("volume must be between -144 and 0 dB")
	}
	r.mu.Lock()
	current := r.current
	if current == nil || r.state != StateRunning {
		r.mu.Unlock()
		return ErrNotRunning
	}
	service, ok := current.service.(*protocolService)
	r.mu.Unlock()
	if !ok {
		return ErrControlUnsupported
	}
	owner := service.player.Current()
	if owner == nil {
		return ErrNoSender
	}
	if owner.NotifyVolume == nil {
		return ErrControlUnsupported
	}
	return owner.NotifyVolume(ctx, db)
}

func validVolume(db float64) bool {
	return db >= -144 && db <= 0 && !math.IsNaN(db) && !math.IsInf(db, 0)
}

func (s *protocolService) Control(ctx context.Context, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, claimID := s.player.Ownership()
	paused := s.pausedSender()
	if owner == nil {
		if paused == nil {
			return ErrNoSender
		}
		switch action {
		case "pause", "resume", "stop", "next", "previous", "restart":
			return paused.send(ctx, action)
		default:
			return ErrControlUnsupported
		}
	}
	switch action {
	case "volume_up", "volume_down":
		db := s.player.Volume()
		if action == "volume_up" {
			db = min(0, max(-60, db)+3)
		} else {
			db -= 3
			if db < -60 {
				db = -144
			}
		}
		return s.setOwnerVolume(ctx, owner, db)
	case "pause", "resume", "stop", "next", "previous", "restart":
		if owner.RemoteControl == nil {
			return ErrControlUnsupported
		}
		local, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(owner.Context(), cancel)
		defer stop()
		defer cancel()
		if owner.Context().Err() != nil {
			return ErrNoSender
		}
		err := owner.RemoteControl(local, action)
		if err == nil && action == "pause" && owner.RemoteAvailable != nil && owner.RemoteAvailable() {
			s.pausedMu.Lock()
			_, currentClaim := s.player.Ownership()
			if currentClaim == claimID {
				s.pausedRemote = &pausedRemote{owner: owner, claimID: claimID, send: owner.RemoteControl, available: owner.RemoteAvailable}
			}
			s.pausedMu.Unlock()
		}
		return err
	default:
		return ErrControlUnsupported
	}
}

func (s *protocolService) SetVolume(ctx context.Context, db float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validVolume(db) {
		return fmt.Errorf("invalid volume")
	}
	return s.setOwnerVolume(ctx, s.player.Current(), db)
}
func (s *protocolService) setOwnerVolume(ctx context.Context, owner *audio.Server, db float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == nil || !s.player.SetVolumeFor(owner, db) {
		return ErrNoSender
	}
	// Phone slider notification is advisory; PCM gain is already committed.
	if owner.NotifyVolume != nil {
		_ = owner.NotifyVolume(ctx, db)
	}
	return nil
}
