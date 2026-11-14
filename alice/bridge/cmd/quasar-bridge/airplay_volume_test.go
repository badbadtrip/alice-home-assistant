package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

type volumeRig struct {
	mu      sync.Mutex
	level   float64
	sets    []int
	reports []float64
	busy    bool
}

func (r *volumeRig) unified() *unifiedVolume {
	return &unifiedVolume{
		set: func(_ context.Context, level int) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sets = append(r.sets, level)
			r.level = float64(level)
			return nil
		},
		level: func() (float64, bool) { r.mu.Lock(); defer r.mu.Unlock(); return r.level, true },
		report: func(_ context.Context, db float64) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reports = append(r.reports, db)
			return nil
		},
		busy: func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.busy },
	}
}

func (r *volumeRig) snapshot() ([]int, []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.sets...), append([]float64(nil), r.reports...)
}

func (r *volumeRig) setLevel(level float64) { r.mu.Lock(); r.level = level; r.mu.Unlock() }

func fastVolume(t *testing.T) {
	t.Helper()
	apply, quiet, rest := phoneApply, phoneQuiet, speakerRest
	phoneApply, phoneQuiet, speakerRest = 20*time.Millisecond, 150*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { phoneApply, phoneQuiet, speakerRest = apply, quiet, rest })
}

func TestVolumeScales(t *testing.T) {
	for _, c := range []struct {
		level float64
		db    float64
	}{{0, -144}, {10, 0}, {5, -15}, {1, -27}} {
		if got := levelToDB(c.level); got != c.db {
			t.Errorf("levelToDB(%v) = %v, want %v", c.level, got, c.db)
		}
	}
	for _, c := range []struct {
		db    float64
		level int
	}{{0, 10}, {-15, 5}, {-29.9, 1}, {-30, 1}, {-1.875, 9}} {
		if got := dbToLevel(c.db); got != c.level {
			t.Errorf("dbToLevel(%v) = %v, want %v", c.db, got, c.level)
		}
	}
}

// A drag as logged on Midi: a value every ~40 ms down to the bottom and silence.
func TestDragSendsOnlyTheLatestLevelAndNeverMutesAlice(t *testing.T) {
	fastVolume(t)
	rig := &volumeRig{level: 7}
	u := rig.unified()
	for _, db := range []float64{-9.75, -11.6, -13.5, -15.4, -17.3, -19.1, -21, -22.9, -24.8, -28.5, -144} {
		u.fromSender(db)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	sets, reports := rig.snapshot()
	if len(sets) == 0 || len(sets) > 3 || sets[len(sets)-1] != 1 {
		t.Fatalf("levels sent to Alice %v", sets)
	}
	if len(reports) != 0 {
		t.Fatalf("sent back to the phone during its own drag: %v", reports)
	}
}

func TestMixerCatchingUpIsNotSentBack(t *testing.T) {
	fastVolume(t)
	rig := &volumeRig{level: 9}
	u := rig.unified()
	u.fromSender(-24.4) // the phone asks for 2 …
	for _, level := range []float64{8, 6, 4, 2} {
		u.changed(level) // … and the mixer passes 8, 6, 4 on the way
	}
	time.Sleep(phoneQuiet + 100*time.Millisecond)
	if _, reports := rig.snapshot(); len(reports) != 0 {
		t.Fatalf("the slider was pulled back: %v", reports)
	}
}

func TestSpeakerChangeMovesTheQuietPhone(t *testing.T) {
	fastVolume(t)
	rig := &volumeRig{level: 5}
	u := rig.unified()
	u.speaker() // the phone connected at 5
	rig.setLevel(6)
	u.changed(5.5) // "громче": a quick run settles to the last value
	u.changed(6)
	waitFor(t, func() bool { _, r := rig.snapshot(); return len(r) > 0 })
	time.Sleep(60 * time.Millisecond)
	if _, reports := rig.snapshot(); len(reports) != 1 || reports[0] != levelToDB(6) {
		t.Fatalf("reports %v", reports)
	}
	u.changed(6) // the phone already shows 6
	time.Sleep(60 * time.Millisecond)
	if _, reports := rig.snapshot(); len(reports) != 1 {
		t.Fatalf("sent again: %v", reports)
	}
}

func TestSpeakerVolumeWaitsForAlice(t *testing.T) {
	fastVolume(t)
	rig := &volumeRig{level: 6, busy: true}
	u := rig.unified()
	u.changed(6)
	time.Sleep(speakerRest + 50*time.Millisecond)
	if _, reports := rig.snapshot(); len(reports) != 0 {
		t.Fatalf("sent while Alice was speaking: %v", reports)
	}
	rig.mu.Lock()
	rig.busy, rig.level = false, 7 // she restored or changed it meanwhile
	rig.mu.Unlock()
	u.aliceIdle()
	if _, reports := rig.snapshot(); len(reports) != 1 || reports[0] != levelToDB(7) {
		t.Fatalf("reports after Alice %v", reports)
	}
	u.aliceIdle() // nothing held: nothing sent
	if _, reports := rig.snapshot(); len(reports) != 1 {
		t.Fatalf("sent twice: %v", reports)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !ok() {
		time.Sleep(5 * time.Millisecond)
	}
}
