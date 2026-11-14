package main

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"
)

// unifiedVolume makes the phone's AirPlay slider and the speaker's volume one
// control, as on a HomePod. The slider sets Alice's level (Master, 0-10) and the
// receiver stays at 0 dB; voice and buttons move the slider on the phone. A
// slider at zero silences only AirPlay: Master at 0 would silence Alice too.
type unifiedVolume struct {
	// set changes Alice's own level (the bridge's volume push).
	set func(ctx context.Context, level int) error
	// level is the speaker's level now, 0-10 in half steps.
	level func() (float64, bool)
	// report moves the sender's slider; busy says Alice is listening or speaking.
	report func(ctx context.Context, db float64) error
	busy   func() bool

	mu      sync.Mutex
	sent    int       // level the phone asked for last
	sentAt  time.Time // when; the mixer echo of it is not sent back
	pending *time.Timer
	held    bool // a change came while Alice was busy
}

const (
	volumeEcho     = 3 * time.Second
	volumeSettle   = 300 * time.Millisecond
	silenceDB      = -144.0
	sliderBottomDB = -30.0
)

// levelToDB maps Alice's 0-10 onto the AirPlay slider (-30..0 dB, silence below).
func levelToDB(level float64) float64 {
	if level <= 0 {
		return silenceDB
	}
	return sliderBottomDB + -sliderBottomDB*math.Min(level, 10)/10
}

// dbToLevel is the reverse, never below 1: the phone cannot mute Alice.
func dbToLevel(db float64) int {
	level := int(math.Round((db - sliderBottomDB) / -sliderBottomDB * 10))
	return min(10, max(1, level))
}

// fromSender takes the phone's slider. It runs on the receiver's RTSP goroutine,
// so the push to Alice goes out in the background.
func (u *unifiedVolume) fromSender(db float64) bool {
	if db <= silenceDB {
		return true // the receiver mutes itself; Alice keeps her level
	}
	want := dbToLevel(db)
	if now, ok := u.level(); ok && int(math.Round(now)) == want {
		return true
	}
	u.mu.Lock()
	u.sent, u.sentAt = want, time.Now()
	u.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := u.set(ctx, want); err != nil {
			slog.Warn("airplay_volume_failed", "level", want, "error", err)
		}
	}()
	return true
}

// speaker answers the phone's question about the volume when it connects.
func (u *unifiedVolume) speaker() (float64, bool) {
	level, ok := u.level()
	if !ok {
		return 0, false
	}
	return levelToDB(level), true
}

// changed follows the speaker's volume: buttons and voice. Quick runs of
// changes settle first; the echo of the phone's own request is not sent back.
func (u *unifiedVolume) changed(level float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if int(math.Round(level)) == u.sent && time.Since(u.sentAt) < volumeEcho {
		return
	}
	if u.pending != nil {
		u.pending.Stop()
	}
	u.pending = time.AfterFunc(volumeSettle, u.flush)
}

// aliceIdle sends a change that waited for Alice to finish.
func (u *unifiedVolume) aliceIdle() {
	u.mu.Lock()
	held := u.held
	u.held = false
	u.mu.Unlock()
	if held {
		u.flush()
	}
}

func (u *unifiedVolume) flush() {
	// Alice may change Master herself while she speaks; wait for the result.
	if u.busy != nil && u.busy() {
		u.mu.Lock()
		u.held = true
		u.mu.Unlock()
		return
	}
	level, ok := u.level()
	if !ok || u.report == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// No sender or one without the event channel: nothing to move.
	_ = u.report(ctx, levelToDB(level))
}
