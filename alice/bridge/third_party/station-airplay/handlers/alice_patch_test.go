package handlers

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/skrashevich/station-airplay/audio"
	"github.com/skrashevich/station-airplay/rtsp"
)

func volumeHandler(t *testing.T, hooked *[]float64, speaker float64) *Rstp {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &audio.Player{Context: ctx, Logger: log.New(io.Discard, "", 0)}
	p.SetVolume(0)
	if hooked != nil {
		p.SenderVolume = func(db float64) bool { *hooked = append(*hooked, db); return true }
		p.SpeakerVolume = func() (float64, bool) { return speaker, true }
	}
	return &Rstp{logger: p.Logger, streams: map[string]*audio.Server{"s": audio.NewServer(p)}, player: p}
}

func TestSenderSliderGoesToTheSpeaker(t *testing.T) {
	var hooked []float64
	r := volumeHandler(t, &hooked, -15)
	resp, err := r.OnSetParameterWeb(&rtsp.Request{Body: []byte("volume: -12.000000\r\n")})
	if err != nil || resp.StatusCode != rtsp.StatusOK {
		t.Fatal(resp, err)
	}
	if len(hooked) != 1 || hooked[0] != -12 || r.volume != -12 {
		t.Fatalf("hook %v volume %v", hooked, r.volume)
	}
	get, _ := r.OnGetParameterWeb(&rtsp.Request{})
	if !strings.Contains(string(get.Body), "volume: -15.000000") {
		t.Fatalf("the sender starts from the speaker's volume: %q", get.Body)
	}
}

func TestWithoutHooksTheSliderStaysLocal(t *testing.T) {
	r := volumeHandler(t, nil, 0)
	if _, err := r.OnSetParameterWeb(&rtsp.Request{Body: []byte("volume: -12.000000\r\n")}); err != nil {
		t.Fatal(err)
	}
	get, _ := r.OnGetParameterWeb(&rtsp.Request{})
	if !strings.Contains(string(get.Body), "volume: -12.000000") {
		t.Fatalf("upstream behaviour changed: %q", get.Body)
	}
}
