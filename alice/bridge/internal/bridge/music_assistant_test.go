package bridge

import (
	"context"
	"testing"

	"quasarbridge/internal/sendspin"
)

type maHome struct{ source, provider string }

func (maHome) Handle(context.Context, string) (string, error) { return "", nil }
func (h maHome) MusicSource() string                          { return h.source }
func (h maHome) MusicProvider() string                        { return h.provider }

type fakeMA struct {
	streaming bool
	controls  []string
	plays     [][2]string
	playErr   error
}

func (f *fakeMA) Streaming() bool { return f.streaming }
func (f *fakeMA) Control(_ context.Context, action string) error {
	f.controls = append(f.controls, action)
	return nil
}
func (f *fakeMA) Play(_ context.Context, kind, query string) error {
	f.plays = append(f.plays, [2]string{kind, query})
	return f.playErr
}

func TestMusicAssistantTakesUnnamedMusicWhenChosen(t *testing.T) {
	ma := &fakeMA{}
	e := &Engine{Home: maHome{"alternative", "music_assistant"}, MusicAssistant: ma}
	ctx := context.Background()
	for _, text := range []string{"включи песню богемская рапсодия", "включи группу кино", "включи музыку"} {
		decision := e.Decide(ctx, text)
		if decision.Source != "ma_play" {
			t.Fatalf("%q went to %+v", text, decision)
		}
		if _, err := e.ResolveDecision(ctx, text, decision); err != nil {
			t.Fatal(err)
		}
	}
	want := [][2]string{{"track", "богемская рапсодия"}, {"artist", "кино"}, {"", ""}}
	if len(ma.plays) != 3 || ma.plays[0] != want[0] || ma.plays[1] != want[1] || ma.plays[2] != want[2] {
		t.Fatalf("plays %v", ma.plays)
	}
}

func TestMusicAssistantLeavesNamedServicesAndStockMusic(t *testing.T) {
	ma := &fakeMA{}
	ctx := context.Background()
	chosen := &Engine{Home: maHome{"alternative", "music_assistant"}, MusicAssistant: ma}
	if d := chosen.Decide(ctx, "включи кино в спотифай"); d.Source == "ma_play" {
		t.Fatal("a named service must not go to Music Assistant")
	}
	stock := &Engine{Home: maHome{"yandex", "music_assistant"}, MusicAssistant: ma}
	if d := stock.Decide(ctx, "включи песню кино"); d.Source == "ma_play" {
		t.Fatal("stock music must stay with Alice")
	}
	unset := &Engine{Home: maHome{"alternative", "music_assistant"}}
	if d := unset.Decide(ctx, "включи музыку"); d.Source == "ma_play" {
		t.Fatal("no player, no Music Assistant route")
	}
}

func TestMusicAssistantControlsOnlyWhilePlaying(t *testing.T) {
	ma := &fakeMA{}
	e := &Engine{Home: maHome{"yandex", ""}, MusicAssistant: ma}
	ctx := context.Background()
	if d := e.Decide(ctx, "пауза"); d.Source == "ma_control" {
		t.Fatal("pause while Music Assistant is silent belongs to Alice")
	}
	ma.streaming = true
	for text, action := range map[string]string{"пауза": "pause", "дальше": "next", "продолжи": "play", "стоп": "stop"} {
		d := e.Decide(ctx, text)
		if d.Source != "ma_control" {
			t.Fatalf("%q went to %+v", text, d)
		}
		ma.controls = nil
		if _, err := e.ResolveDecision(ctx, text, d); err != nil || len(ma.controls) != 1 || ma.controls[0] != action {
			t.Fatalf("%q sent %v (%v)", text, ma.controls, err)
		}
	}
	if d := e.Decide(ctx, "сначала"); d.Source == "ma_control" {
		t.Fatal("restart has no Music Assistant command")
	}
}

func TestMusicAssistantNotFoundIsSpoken(t *testing.T) {
	ma := &fakeMA{playErr: sendspin.ErrNotFound}
	e := &Engine{Home: maHome{"alternative", "music_assistant"}, MusicAssistant: ma}
	ctx := context.Background()
	r, err := e.ResolveDecision(ctx, "включи песню нет такой", RouteDecision{Route: "playback", Source: "ma_play"})
	if err != nil || r.Answer == "" {
		t.Fatalf("answer %q err %v", r.Answer, err)
	}
}
