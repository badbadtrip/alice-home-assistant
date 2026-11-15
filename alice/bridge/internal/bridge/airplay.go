package bridge

import (
	"context"
	"errors"

	"quasarbridge/internal/playback"
)

// AirPlayPlayer is the speaker's AirPlay receiver: a phone or a Mac that owns
// playback takes pause, resume, next, previous and stop over AirPlay itself.
type AirPlayPlayer interface {
	// Connected: a sender owns a session, playing or paused.
	Connected() bool
	Playing() bool
	Control(ctx context.Context, action string) error
}

// airPlayActions are the spoken controls the receiver passes to the sender.
// "Стоп" pauses: a stopped sender drops the session, and "продолжи" would have
// nobody to resume (station-airplay does the same). Switching AirPlay off stops.
var airPlayActions = map[string]string{"pause": "pause", "resume": "resume", "next": "next", "previous": "previous", "stop": "pause"}

// ErrAirPlayRefused: the sender is there but did not accept the command.
var ErrAirPlayRefused = errors.New("airplay sender refused the command")

func (e *Engine) airPlayConnected() bool { return e.AirPlay != nil && e.AirPlay.Connected() }
func (e *Engine) airPlayPlaying() bool   { return e.AirPlay != nil && e.AirPlay.Playing() }

// controlTarget picks who takes a player command: what sounds now, then a
// paused AirPlay session (so "продолжи" resumes the phone), then nobody.
func (e *Engine) controlTarget(action string) string {
	switch {
	case e.airPlayPlaying() && airPlayActions[action] != "":
		return "airplay"
	case e.maStreaming() && maActions[action] != "":
		return "music_assistant"
	case e.airPlayConnected() && airPlayActions[action] != "":
		return "airplay"
	}
	return ""
}

// pauseAirPlay quiets the phone when other music takes the speaker.
func (e *Engine) pauseAirPlay(ctx context.Context) {
	if e.airPlayPlaying() {
		_ = e.AirPlay.Control(ctx, "pause")
	}
}

// AirPlaySwitcher turns the receiver on and off by voice (10.6). The bridge
// reports a voice "off" so the panel does not switch AirPlay back on.
type AirPlaySwitcher interface {
	SetEnabled(ctx context.Context, on bool) error
	Name() string
}

func (e *Engine) switchAirPlay(ctx context.Context, text string) string {
	on, _ := playback.AirPlaySwitch(text)
	switcher := e.AirPlay.(AirPlaySwitcher)
	if err := switcher.SetEnabled(ctx, on); err != nil {
		return Reply("airplay_switch_failed", "Не получилось переключить AirPlay.")
	}
	switch {
	case !on:
		return Reply("airplay_off", "AirPlay выключен.")
	case e.AirPlay.Connected():
		return Reply("airplay_on_playing", "AirPlay включён, играет iPhone.")
	}
	return Reply("airplay_on", "AirPlay включён. Выберите «"+switcher.Name()+"» на iPhone.")
}
