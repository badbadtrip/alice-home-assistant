package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	airplay "github.com/skrashevich/station-airplay"
	"github.com/skrashevich/station-airplay/audio"
)

type fakeReceiver struct {
	starts, stops int
	failures      int
	// status replaces the default snapshot when set.
	status   *airplay.Status
	controls []string
	mu       sync.Mutex
	ducks    []float64
}

func (f *fakeReceiver) Start(context.Context) error {
	f.starts++
	if f.failures > 0 {
		f.failures--
		return errors.New("not ready")
	}
	return nil
}
func (f *fakeReceiver) Stop(context.Context) error { f.stops++; return nil }
func (f *fakeReceiver) SetDuck(factor float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ducks = append(f.ducks, factor)
	return nil
}
func (f *fakeReceiver) duckLog() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.ducks...)
}
func (f *fakeReceiver) Control(_ context.Context, action string) error {
	f.controls = append(f.controls, action)
	return nil
}
func (f *fakeReceiver) Status() airplay.Status {
	if f.status != nil {
		return *f.status
	}
	return airplay.Status{State: airplay.StateRunning, Name: "Алиса Кухня", Connected: true}
}

func testAirPlay(t *testing.T, f *fakeReceiver) *airPlayControl {
	t.Helper()
	return &airPlayControl{receiver: f, app: context.Background(), marker: filepath.Join(t.TempDir(), "airplay.enabled"), retry: time.Millisecond}
}

func airPlayCall(c *airPlayControl, action string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/airplay?action="+action, nil))
	return w
}

func TestAirPlayChoiceSurvivesRestart(t *testing.T) {
	f := &fakeReceiver{}
	c := testAirPlay(t, f)
	c.resume()
	if f.starts != 0 {
		t.Fatal("receiver started without a saved choice")
	}
	if w := airPlayCall(c, "start"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	f.starts = 0
	f.failures = 2
	c.resume()
	if f.starts != 3 {
		t.Fatalf("boot retries: %d", f.starts)
	}
	if w := airPlayCall(c, "stop"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) || f.stops != 1 {
		t.Fatalf("%d %s stops=%d", w.Code, w.Body, f.stops)
	}
}

func TestAirPlayFailedStartSavesNothing(t *testing.T) {
	c := testAirPlay(t, &fakeReceiver{failures: 1})
	if w := airPlayCall(c, "start"); w.Code != http.StatusBadGateway || c.enabled() {
		t.Fatalf("%d enabled=%v", w.Code, c.enabled())
	}
	if w := airPlayCall(c, "reboot"); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
}

func TestAirPlayFollowsNewAddress(t *testing.T) {
	f := &fakeReceiver{status: &airplay.Status{State: airplay.StateRunning, Address: "192.168.1.59:7000"}}
	c := testAirPlay(t, f)
	if w := airPlayCall(c, "start"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.app = ctx
	addresses := make(chan string, 4)
	addresses <- "192.168.1.59" // unchanged: nothing happens
	addresses <- "192.168.1.77" // changed: one restart
	current := func() string {
		select {
		case a := <-addresses:
			return a
		default:
			cancel()
			return ""
		}
	}
	c.followAddress(time.Millisecond, current)
	if f.stops != 1 || f.starts != 2 {
		t.Fatalf("stops=%d starts=%d", f.stops, f.starts)
	}
}

func TestAirPlayKeepsSessionOnNewAddress(t *testing.T) {
	f := &fakeReceiver{status: &airplay.Status{State: airplay.StateRunning, Address: "192.168.1.59:7000", Connected: true}}
	c := testAirPlay(t, f)
	airPlayCall(c, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c.app = ctx
	c.followAddress(time.Millisecond, func() string { return "192.168.1.77" })
	if f.stops != 0 {
		t.Fatal("restarted while a sender owns playback")
	}
}

func TestAirPlayNameFitsMDNS(t *testing.T) {
	if got := airPlayName(""); got != "Алиса" {
		t.Fatalf("%q", got)
	}
	if got := airPlayName("  Кухня  "); got != "Алиса Кухня" {
		t.Fatalf("%q", got)
	}
	long := airPlayName("Детская комната на втором этаже у лестницы")
	if len(long) > 63 || !utf8.ValidString(long) || !strings.HasPrefix(long, "Алиса Детская") {
		t.Fatalf("%q (%d bytes)", long, len(long))
	}
	if _, err := airplay.New(airplay.Config{Name: long, DataDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestMusicHelperWithoutAirPlay(t *testing.T) {
	if _, err := newPCMHelper(nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

type fakeMusic struct {
	streaming bool
	controls  []string
}

func (f *fakeMusic) Streaming() bool { return f.streaming }
func (f *fakeMusic) Control(_ context.Context, action string) error {
	f.controls = append(f.controls, action)
	return nil
}

func TestOneSoundPausesWhoeverPlayedFirst(t *testing.T) {
	f := &fakeReceiver{status: &airplay.Status{State: airplay.StateRunning}}
	c := testAirPlay(t, f)
	music := &fakeMusic{}
	var one oneSound
	ctx := context.Background()
	f.status.Playing = true
	one.check(ctx, c, music)
	music.streaming = true // music started over the phone
	one.check(ctx, c, music)
	if len(f.controls) != 1 || f.controls[0] != "pause" || len(music.controls) != 0 {
		t.Fatalf("phone %v music %v", f.controls, music.controls)
	}
	f.status.Playing = false
	one.check(ctx, c, music)
	f.status.Playing = true // the phone started over the music
	one.check(ctx, c, music)
	if len(music.controls) != 1 || music.controls[0] != "pause" {
		t.Fatalf("music %v", music.controls)
	}
	one.check(ctx, c, music) // both still reported: nothing new
	if len(music.controls) != 1 || len(f.controls) != 1 {
		t.Fatalf("repeated: phone %v music %v", f.controls, music.controls)
	}
}

func TestAirPlayDucksUnderAliceAndComesBack(t *testing.T) {
	f := &fakeReceiver{}
	c := testAirPlay(t, f)
	waitDuck := func(want float64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if d := f.duckLog(); len(d) > 0 && d[len(d)-1] == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("duck never reached %v: %v", want, f.duckLog())
	}
	c.SetAliceState("listening")
	waitDuck(airPlayDuck)
	steps := len(f.duckLog())
	c.SetAliceState("speaking") // still Alice: no new ramp
	time.Sleep(50 * time.Millisecond)
	if len(f.duckLog()) != steps {
		t.Fatalf("ramped again while Alice kept talking: %v", f.duckLog())
	}
	c.SetAliceState("idle")
	waitDuck(1)
	d := f.duckLog()
	for i := 1; i < steps; i++ {
		if d[i] >= d[i-1] {
			t.Fatalf("down ramp not falling: %v", d)
		}
	}
}

func TestPlayerDuckScalesGainWithoutVolume(t *testing.T) {
	p := &audio.Player{}
	p.SetVolume(0)
	p.SetDuck(0.15)
	if g := p.Gain(); g < 0.149 || g > 0.151 || p.Volume() != 0 {
		t.Fatalf("gain %v volume %v", g, p.Volume())
	}
	p.SetDuck(1)
	if p.Gain() != 1 {
		t.Fatalf("restored gain %v", p.Gain())
	}
}

func TestAirPlayDuckReleasesWhenAliceSticks(t *testing.T) {
	saved := duckHold
	duckHold = 200 * time.Millisecond
	defer func() { duckHold = saved }()
	f := &fakeReceiver{}
	c := testAirPlay(t, f)
	c.SetAliceState("speaking")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d := f.duckLog(); len(d) > 0 && d[len(d)-1] == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d := f.duckLog(); len(d) == 0 || d[len(d)-1] != 1 {
		t.Fatalf("music never came back: %v", d)
	}
	steps := len(f.duckLog())
	c.SetAliceState("idle") // the real end changes nothing more
	time.Sleep(50 * time.Millisecond)
	if len(f.duckLog()) != steps {
		t.Fatalf("restored twice: %v", f.duckLog())
	}
	c.SetAliceState("listening") // and the next request ducks again
	reached := func() bool {
		for _, x := range f.duckLog()[steps:] {
			if x == airPlayDuck {
				return true
			}
		}
		return false
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !reached() {
		time.Sleep(5 * time.Millisecond)
	}
	if !reached() {
		t.Fatalf("no duck after release: %v", f.duckLog())
	}
}
