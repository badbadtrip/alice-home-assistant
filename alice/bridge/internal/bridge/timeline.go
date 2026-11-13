package bridge

import (
	"log/slog"
	"sync"
	"time"
)

// Timeline is where one request spent its time, from the first byte of the request
// to the moment the speaker got something to say. Zero means the step did not happen.
type Timeline struct {
	At       string `json:"at"`
	Request  string `json:"request"`
	Route    string `json:"route"`
	Source   string `json:"source"`
	ASRMS    int64  `json:"asr_ms"`    // request start to final recognition
	DecideMS int64  `json:"decide_ms"` // choosing who answers
	ExecMS   int64  `json:"exec_ms"`   // the chosen executor's work
	VoiceMS  int64  `json:"voice_ms"`  // answer sent to speech or cloud answer ready
	TotalMS  int64  `json:"total_ms"`  // final recognition (or text) to voice
}

type requestClock struct {
	born, asr, decide, decided, answered time.Time
	source                               string
	logged                               bool
}

const timelineKeep = 50

var timelines struct {
	sync.Mutex
	items []Timeline
}

// RecentTimelines returns the last requests, newest last, for /timeline.
func RecentTimelines() []Timeline {
	timelines.Lock()
	defer timelines.Unlock()
	return append([]Timeline(nil), timelines.items...)
}

func since(from, to time.Time) int64 {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return to.Sub(from).Milliseconds()
}

// finishClock records the request once, when its voice is ready. Callers hold n.mu.
func finishClock(p *nativeRequest, now time.Time) {
	c := &p.clock
	if c.logged || c.born.IsZero() {
		return
	}
	c.logged = true
	heard := c.asr
	if heard.IsZero() {
		heard = c.born
	}
	t := Timeline{At: now.UTC().Format(time.RFC3339Nano), Request: requestTag(p.id), Route: p.route, Source: c.source,
		ASRMS: since(c.born, c.asr), DecideMS: since(c.decide, c.decided), ExecMS: since(c.decided, c.answered),
		VoiceMS: since(c.answered, now), TotalMS: since(heard, now)}
	slog.Info("native_timeline", "request", t.Request, "route", t.Route, "source", t.Source, "asr_ms", t.ASRMS,
		"decide_ms", t.DecideMS, "exec_ms", t.ExecMS, "voice_ms", t.VoiceMS, "total_ms", t.TotalMS)
	timelines.Lock()
	timelines.items = append(timelines.items, t)
	if len(timelines.items) > timelineKeep {
		timelines.items = timelines.items[len(timelines.items)-timelineKeep:]
	}
	timelines.Unlock()
}
