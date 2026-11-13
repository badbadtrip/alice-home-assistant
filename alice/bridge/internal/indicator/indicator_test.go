package indicator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func active(c *Controller) int { c.mu.Lock(); defer c.mu.Unlock(); return c.active }

func TestLocalTurnDoesNotAcquireRing(t *testing.T) {
	c := &Controller{}
	_, finish := WithTurn(t.Context())
	finish()
	if active(c) != 0 {
		t.Fatal("local turn claimed LEDs")
	}
}

func TestIntensityAndWaveforms(t *testing.T) {
	brightness := 80
	cfg := Config{Enabled: true, Color: "#00bfff", Brightness: &brightness, PeriodMS: 2400}
	if !cfg.valid() || cfg.Level(time.Second) != 80 {
		t.Fatal("steady brightness changed")
	}
	cfg.Mode = "breathe"
	if cfg.Level(0) != 80 || cfg.Level(1200*time.Millisecond) != 20 {
		t.Fatal("breathing range is wrong")
	}
	cfg.Mode = "double"
	if cfg.Level(0) != 80 || cfg.Level(576*time.Millisecond) != 80 || cfg.Level(1440*time.Millisecond) > 17 {
		t.Fatal("double pulse peaks or floor are wrong")
	}
	for _, mode := range []string{"steady", "breathe", "double"} {
		cfg.Mode = mode
		for ms := 0; ms < 5000; ms += 13 {
			if value := cfg.Level(time.Duration(ms) * time.Millisecond); value < 0 || value > brightness {
				t.Fatalf("out of range: %d", value)
			}
		}
	}
	cfg.Mode = "bad"
	if cfg.valid() {
		t.Fatal("unknown effect accepted")
	}
	cfg.Mode = "steady"
	cfg.PeriodMS = 799
	if cfg.valid() {
		t.Fatal("excessive pulse rate accepted")
	}
	cfg.PeriodMS = 2400
	brightness = 101
	if cfg.valid() {
		t.Fatal("excessive intensity accepted")
	}
	if !(Config{Color: "#00bfff"}).valid() {
		t.Fatal("legacy saved color rejected")
	}
}

func TestModelToolModelKeepsOneOwnershipUntilTurnEnds(t *testing.T) {
	c := &Controller{}
	ctx, finish := WithTurn(t.Context())
	c.Request(ctx)()
	if active(c) != 1 {
		t.Fatal("first model request did not claim ring")
	}
	// Returning from HTTP and waiting for tools must not restore the firmware.
	c.Request(ctx)()
	nested, nestedFinish := WithTurn(ctx)
	c.Request(nested)()
	nestedFinish()
	if active(c) != 1 {
		t.Fatal("model steps reacquired or released LEDs")
	}
	finish()
	finish()
	if active(c) != 0 {
		t.Fatal("turn completion did not release LEDs")
	}
	c.Request(ctx)()
	if active(c) != 0 {
		t.Fatal("late completion reclaimed LEDs")
	}
}

func TestCancelReleasesOnlyItsOwnTurn(t *testing.T) {
	c := &Controller{}
	parent, cancel := context.WithCancel(t.Context())
	first, finishFirst := WithTurn(parent)
	defer finishFirst()
	second, finishSecond := WithTurn(t.Context())
	defer finishSecond()
	c.Request(first)()
	c.Request(second)()
	cancel()
	until := time.Now().Add(time.Second)
	for active(c) != 1 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if active(c) != 1 {
		t.Fatal("cancel did not retain the overlapping turn")
	}
	finishSecond()
	if active(c) != 0 {
		t.Fatal("last turn did not release ring")
	}
}
func TestPlumRainbowFrames(t *testing.T) {
	brightness := 80
	cfg := Config{Enabled: true, Color: "#00d5ff", Brightness: &brightness, Mode: "rainbow", PeriodMS: 2400}
	if !cfg.valid() {
		t.Fatal("rainbow rejected")
	}
	want := [9]int{128, 0, 0, 0, 128, 0, 0, 0, 128}
	if got := cfg.Frame(0); got != want {
		t.Fatalf("zones are not phase shifted: %v", got)
	}
	if cfg.Frame(2400*time.Millisecond) != want {
		t.Fatal("rainbow does not loop")
	}
	for ms := 0; ms < 5000; ms += 17 {
		for _, v := range cfg.Frame(time.Duration(ms) * time.Millisecond) {
			if v < 0 || v > 128 {
				t.Fatalf("frame exceeds brightness limit: %d", v)
			}
		}
	}
	cfg.Mode = "steady"
	frame := cfg.Frame(0)
	for zone := 1; zone < 3; zone++ {
		for c := 0; c < 3; c++ {
			if frame[zone*3+c] != frame[c] {
				t.Fatal("solid color differs across zones")
			}
		}
	}
}

func TestJournalKeepsOnlyChanges(t *testing.T) {
	var j ringJournal
	start := time.Now()
	j.note(start, "lamp", false, false)
	j.note(start.Add(time.Second), "lamp", false, false)
	j.note(start.Add(2*time.Second), "music", true, true)
	j.note(start.Add(2300*time.Millisecond), "lamp", false, true)
	got := j.list()
	if len(got) != 3 || got[1].To != "music" || got[2].HeldMS != 300 || !got[2].Player {
		t.Fatalf("%+v", got)
	}
}

func TestQuickModelCallsDoNotLightTheRing(t *testing.T) {
	c := &Controller{}
	done := c.Begin()
	if c.thinking() {
		t.Fatal("a call that just began must not light the ring yet")
	}
	c.mu.Lock()
	c.thinkingSince = time.Now().Add(-ThinkDelay)
	c.mu.Unlock()
	if !c.thinking() {
		t.Fatal("a call running past the delay lights the ring")
	}
	done()
	if c.thinking() {
		t.Fatal("finished calls leave the ring")
	}
}

func TestShortFirmwareStatesDoNotTakeTheRing(t *testing.T) {
	c := &Controller{}
	c.SetVoice("idle")
	c.SetVoice("listening")
	now := time.Now()
	if c.yielding(now, false) {
		t.Fatal("a fresh listening state may be a false blip")
	}
	if !c.yielding(now.Add(YieldAfterVoice), false) {
		t.Fatal("a lasting state belongs to the firmware")
	}
	if c.yielding(now.Add(YieldAfterVoice), true) {
		t.Fatal("with an own look the bridge draws voice itself")
	}
}

func TestBrokenConfigKeepsThePreviousLook(t *testing.T) {
	dir := t.TempDir()
	c := &Controller{Path: filepath.Join(dir, "indicator.json")}
	previous := Config{Enabled: true, Color: "#112233"}
	if got := c.load(previous); got.Enabled {
		t.Fatal("no file means no look")
	}
	_ = os.WriteFile(c.Path, []byte("{broken"), 0o600)
	if got := c.load(previous); got.Color != "#112233" {
		t.Fatalf("a broken read must keep the previous look, got %+v", got)
	}
}
