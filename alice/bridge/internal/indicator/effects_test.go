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
			if p.R > 204 || p.G > 204 || p.B > 204 {
				t.Fatal(mode, p)
			}
		}
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
