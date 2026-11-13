package bridge

import (
	"testing"
	"time"
)

func TestTimelineSplitsTheRequest(t *testing.T) {
	start := time.Now()
	p := &nativeRequest{id: "x", route: "home", clock: requestClock{born: start, asr: start.Add(900 * time.Millisecond),
		decide: start.Add(910 * time.Millisecond), decided: start.Add(915 * time.Millisecond),
		answered: start.Add(1300 * time.Millisecond), source: "voice_phrase"}}
	finishClock(p, start.Add(1500*time.Millisecond))
	finishClock(p, start.Add(9*time.Second)) // logged once
	got := RecentTimelines()
	last := got[len(got)-1]
	if last.ASRMS != 900 || last.DecideMS != 5 || last.ExecMS != 385 || last.VoiceMS != 200 || last.TotalMS != 600 || last.Source != "voice_phrase" {
		t.Fatalf("%+v", last)
	}
	for _, item := range got {
		if item.Request == requestTag("x") && item.TotalMS != 600 {
			t.Fatal("a request is recorded only once")
		}
	}
}
