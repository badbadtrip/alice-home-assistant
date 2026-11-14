package bridge

import (
	"context"
	"errors"
	"testing"
)

type fakeAirPlay struct {
	connected, playing bool
	controls           []string
	err                error
}

func (f *fakeAirPlay) Connected() bool { return f.connected }
func (f *fakeAirPlay) Playing() bool   { return f.playing }
func (f *fakeAirPlay) Control(_ context.Context, action string) error {
	f.controls = append(f.controls, action)
	return f.err
}

func TestAirPlayTakesSpokenControlsWhilePlaying(t *testing.T) {
	ap := &fakeAirPlay{connected: true, playing: true}
	e := &Engine{Home: maHome{"yandex"}, AirPlay: ap}
	ctx := context.Background()
	for text, action := range map[string]string{"пауза": "pause", "дальше": "next", "продолжи": "resume", "стоп": "stop", "назад": "previous"} {
		d := e.Decide(ctx, text)
		if d.Source != "airplay_control" {
			t.Fatalf("%q went to %+v", text, d)
		}
		ap.controls = nil
		if _, err := e.ResolveDecision(ctx, text, d); err != nil || len(ap.controls) != 1 || ap.controls[0] != action {
			t.Fatalf("%q sent %v (%v)", text, ap.controls, err)
		}
	}
	if d := e.Decide(ctx, "сначала"); d.Source == "airplay_control" {
		t.Fatal("restart has no AirPlay command")
	}
}

func TestAirPlayLeavesControlsWithoutSender(t *testing.T) {
	e := &Engine{Home: maHome{"yandex"}, AirPlay: &fakeAirPlay{}}
	if d := e.Decide(context.Background(), "пауза"); d.Source == "airplay_control" {
		t.Fatal("pause with no phone connected belongs to Alice")
	}
}

func TestPlayingSourceWinsOverPausedOne(t *testing.T) {
	ctx := context.Background()
	ap := &fakeAirPlay{connected: true}
	ma := &fakeMA{streaming: true}
	e := &Engine{Home: maHome{"yandex"}, AirPlay: ap, MusicAssistant: ma}
	if d := e.Decide(ctx, "пауза"); d.Source != "ma_control" {
		t.Fatalf("Music Assistant plays, a paused phone waits: %+v", d)
	}
	ma.streaming = false
	if d := e.Decide(ctx, "продолжи"); d.Source != "airplay_control" {
		t.Fatalf("with nothing playing, resume goes to the paused phone: %+v", d)
	}
	ap.playing, ma.streaming = true, true
	if d := e.Decide(ctx, "дальше"); d.Source != "airplay_control" {
		t.Fatalf("a playing phone comes first: %+v", d)
	}
}

func TestRefusedAirPlayCommandIsSpoken(t *testing.T) {
	ap := &fakeAirPlay{connected: true, playing: true, err: errors.New("unsupported")}
	e := &Engine{Home: maHome{"yandex"}, AirPlay: ap}
	r, err := e.ResolveDecision(context.Background(), "дальше", RouteDecision{Route: "playback", Source: "airplay_control"})
	if err != nil || r.Answer == "" {
		t.Fatalf("answer %q err %v", r.Answer, err)
	}
}

func TestPanelControlsReachAirPlay(t *testing.T) {
	ctx := context.Background()
	ap := &fakeAirPlay{connected: true, playing: true}
	e := &Engine{AirPlay: ap}
	if err := e.PlayerControl(ctx, "next"); err != nil || len(ap.controls) != 1 || ap.controls[0] != "next" {
		t.Fatalf("%v %v", ap.controls, err)
	}
	ap.controls = nil
	if err := e.PlayerControl(ctx, "silence"); err != nil || len(ap.controls) != 1 || ap.controls[0] != "pause" {
		t.Fatalf("silence: %v %v", ap.controls, err)
	}
	ap.err = errors.New("no channel")
	if err := e.PlayerControl(ctx, "pause"); !errors.Is(err, ErrAirPlayRefused) {
		t.Fatalf("refusal: %v", err)
	}
	idle := &Engine{AirPlay: &fakeAirPlay{}}
	if err := idle.PlayerControl(ctx, "pause"); err == nil {
		t.Fatal("nothing is playing")
	}
}

func TestStockMusicRequestPausesPhone(t *testing.T) {
	ap := &fakeAirPlay{connected: true, playing: true}
	e := &Engine{Home: maHome{"yandex"}, AirPlay: ap}
	e.Decide(context.Background(), "включи музыку")
	if len(ap.controls) != 1 || ap.controls[0] != "pause" {
		t.Fatalf("controls %v", ap.controls)
	}
}
