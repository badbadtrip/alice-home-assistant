package indicator

import (
	"testing"
	"time"
)

func TestEveryEffectDrawsWithinTheCap(t *testing.T) {
	for mode := range modes {
		frame := Config{Color: "#ffffff", Mode: mode}.Render(700*time.Millisecond, 24)
		if len(frame) != 24 {
			t.Fatal(mode, len(frame))
		}
		for _, p := range frame {
			if p[0] > 204 || p[1] > 204 || p[2] > 204 {
				t.Fatal(mode, p)
			}
		}
	}
}

func TestSmootherShowsValuesBetweenLevels(t *testing.T) {
	var s smoother
	total := 0
	for range 1000 {
		frame := s.next([]Light{{10.25, 0, 0}})
		total += int(frame[0].R)
	}
	if average := float64(total) / 1000; average < 10.1 || average > 10.3 {
		t.Fatal(average)
	}
	for range 100 {
		s.next([]Light{{}})
	}
	if !s.dark() {
		t.Fatal("did not fade out")
	}
}

func TestCometHeadNeverJumps(t *testing.T) {
	cfg := Config{Color: "#ffffff", Mode: "comet", PeriodMS: 2400}
	previous := cfg.Render(0, 24)
	for ms := 5; ms < 5000; ms += 5 {
		frame := cfg.Render(time.Duration(ms)*time.Millisecond, 24)
		for i := range frame {
			if jump := frame[i][0] - previous[i][0]; jump > 20 || jump < -20 {
				t.Fatal(ms, i, jump)
			}
		}
		previous = frame
	}
}

func TestLookPriority(t *testing.T) {
	cfg := Config{Enabled: true, Color: "#111111", Music: &Layer{Enabled: true, Color: "#222222"}, Lamp: &Layer{Enabled: true, Color: "#333333"}}
	for _, test := range []struct {
		thinking, playing bool
		want              string
	}{{true, true, "#111111"}, {false, true, "#222222"}, {false, false, "#333333"}} {
		if look, ok := pick(cfg, test.thinking, test.playing); !ok || look.Color != test.want {
			t.Fatal(test, look)
		}
	}
	if _, ok := pick(Config{Enabled: true, Color: "#111111"}, false, true); ok {
		t.Fatal("ring lit with no look for this state")
	}
}

func TestShortSoundsDoNotCountAsMusic(t *testing.T) {
	var gate musicGate
	for range 5 {
		if gate.update(true) {
			t.Fatal("started too early")
		}
	}
	gate.update(false)
	if !gate.update(true) {
		t.Fatal("brief gap restarted the wait")
	}
	for range 3 {
		gate.update(false)
	}
	if gate.update(false) {
		t.Fatal("music look outlived the music")
	}
}
