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
//
// Measured 2026-11-14 on Midi: a drag sends a value every ~40 ms, and the mixer
// follows a pushed level with a lag, passing through the levels in between.
// So while the phone is being moved it alone is in charge (nothing goes back to
// it), Alice gets only the latest level every phoneApply, and only changes made
// on the speaker with the phone quiet are sent to the phone.
type unifiedVolume struct {
	// set changes Alice's own level (the bridge's volume push).
	set func(ctx context.Context, level int) error
	// level is the speaker's level now, 0-10 in half steps.
	level func() (float64, bool)
	// report moves the sender's slider; busy says Alice is listening or speaking.
	report func(ctx context.Context, db float64) error
	busy   func() bool

	mu         sync.Mutex
	phoneAt    time.Time // last value from the phone
	phoneLevel int       // the level the phone's slider stands for; 0 unknown
	applying   *time.Timer
	pending    *time.Timer
	held       bool // a speaker change came while Alice was busy
}

var (
	phoneApply  = 100 * time.Millisecond // Alice gets the latest slider value this often
	phoneQuiet  = 2 * time.Second        // the phone stays in charge this long after a move
	speakerRest = 300 * time.Millisecond // a run of speaker changes settles first
)

const (
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

// fromSender takes the phone's slider. It runs on the receiver's RTSP goroutine:
// it only records the value; the push to Alice goes out on a short timer.
func (u *unifiedVolume) fromSender(db float64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.phoneAt = time.Now()
	if db <= silenceDB {
		return true // the receiver mutes itself; Alice keeps her level
	}
	u.phoneLevel = dbToLevel(db)
	if u.pending != nil {
		u.pending.Stop() // a speaker change waiting to go out is stale now
		u.pending = nil
	}
	if u.applying == nil {
		u.applying = time.AfterFunc(phoneApply, u.apply)
	}
	return true
}

// apply sends the latest slider value to Alice, once per phoneApply at most.
func (u *unifiedVolume) apply() {
	u.mu.Lock()
	want := u.phoneLevel
	u.applying = nil
	u.mu.Unlock()
	if now, ok := u.level(); ok && int(math.Round(now)) == want {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := u.set(ctx, want); err != nil {
		slog.Warn("airplay_volume_failed", "level", want, "error", err)
		return
	}
	slog.Info("airplay_volume_set", "level", want)
}

// speaker answers the phone's question about the volume when it connects.
func (u *unifiedVolume) speaker() (float64, bool) {
	level, ok := u.level()
	if !ok {
		return 0, false
	}
	u.mu.Lock()
	u.phoneLevel = int(math.Round(level))
	u.mu.Unlock()
	return levelToDB(level), true
}

// changed follows the speaker's volume. Changes while the phone is being moved
// are the mixer catching up with it and stay here.
func (u *unifiedVolume) changed(level float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.phoneAt) < phoneQuiet || int(math.Round(level)) == u.phoneLevel {
		return
	}
	if u.pending != nil {
		u.pending.Stop()
	}
	u.pending = time.AfterFunc(speakerRest, u.flush)
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
	u.mu.Lock()
	if time.Since(u.phoneAt) < phoneQuiet || int(math.Round(level)) == u.phoneLevel {
		u.mu.Unlock()
		return
	}
	u.phoneLevel = int(math.Round(level))
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// No sender or one without the event channel: nothing to move.
	err := u.report(ctx, levelToDB(level))
	slog.Info("airplay_volume_to_phone", "level", level, "error", err)
}
