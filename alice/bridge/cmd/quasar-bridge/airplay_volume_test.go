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

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !ok() {
		time.Sleep(5 * time.Millisecond)
	}
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

func TestPhoneSliderSetsSpeakerButNeverMutesAlice(t *testing.T) {
	rig := &volumeRig{level: 5}
	u := rig.unified()
	if !u.fromSender(-6) {
		t.Fatal("the speaker must take the slider")
	}
	waitFor(t, func() bool { s, _ := rig.snapshot(); return len(s) == 1 })
	u.fromSender(-144) // phone at zero: AirPlay mutes itself, Alice keeps her level
	u.fromSender(-15)  // already the speaker's level: nothing to send
	time.Sleep(30 * time.Millisecond)
	if sets, _ := rig.snapshot(); len(sets) != 1 || sets[0] != 8 {
		t.Fatalf("levels sent %v", sets)
	}
}

func TestSpeakerVolumeMovesPhoneWithoutEcho(t *testing.T) {
	rig := &volumeRig{level: 5}
	u := rig.unified()
	u.fromSender(-6) // the phone asked for 8 …
	rig.mu.Lock()
	rig.level = 8
	rig.mu.Unlock()
	u.changed(8) // … and the mixer reports it: not sent back
	time.Sleep(volumeSettle + 100*time.Millisecond)
	if _, reports := rig.snapshot(); len(reports) != 0 {
		t.Fatalf("echoed the phone's own change: %v", reports)
	}
	rig.mu.Lock()
	rig.level = 3
	rig.mu.Unlock()
	u.changed(4) // buttons: a quick run settles to the last value
	u.changed(3)
	waitFor(t, func() bool { _, r := rig.snapshot(); return len(r) > 0 })
	if _, reports := rig.snapshot(); len(reports) != 1 || reports[0] != levelToDB(3) {
		t.Fatalf("reports %v", reports)
	}
}

func TestSpeakerVolumeWaitsForAlice(t *testing.T) {
	rig := &volumeRig{level: 6, busy: true}
	u := rig.unified()
	u.changed(6)
	time.Sleep(volumeSettle + 100*time.Millisecond)
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
