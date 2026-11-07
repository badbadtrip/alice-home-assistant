package nowplaying

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixed(info *Info, err error) Source {
	return func(context.Context) (*Info, error) { return info, err }
}

func TestPickPrefersSoundOverOrder(t *testing.T) {
	paused, playing := &Info{Source: "spotify", Status: Paused}, &Info{Source: "youtube_music", Status: Playing}
	if got := Pick(context.Background(), fixed(paused, nil), fixed(playing, nil)); got != playing {
		t.Fatalf("got %+v", got)
	}
}

func TestPickKeepsFirstPausedAndSkipsFailures(t *testing.T) {
	first, second := &Info{Source: "spotify", Status: Paused}, &Info{Source: "youtube_music", Status: Paused}
	if got := Pick(context.Background(), fixed(nil, errors.New("down")), fixed(first, nil), fixed(second, nil)); got != first {
		t.Fatalf("got %+v", got)
	}
	if got := Pick(context.Background(), fixed(nil, nil)); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestPickDoesNotWaitForSlowSource(t *testing.T) {
	slow := func(ctx context.Context) (*Info, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	began := time.Now()
	ready := &Info{Source: "airplay", Status: Playing}
	if got := Pick(context.Background(), slow, fixed(ready, nil)); got != ready {
		t.Fatalf("got %+v", got)
	}
	if time.Since(began) > 2*time.Second {
		t.Fatal("the budget was not kept")
	}
}

func TestTrackerHidesAPausedSourceThatDidNotPlayLast(t *testing.T) {
	var tracker Tracker
	spotify := &Info{Source: "spotify", Status: Paused}
	youtube := &Info{Source: "youtube_music", Status: Playing}
	if got := tracker.Pick(context.Background(), fixed(spotify, nil), fixed(nil, nil)); got != nil {
		t.Fatalf("a stale pause showed: %+v", got)
	}
	if got := tracker.Pick(context.Background(), fixed(spotify, nil), fixed(youtube, nil)); got != youtube {
		t.Fatalf("got %+v", got)
	}
	if got := tracker.Pick(context.Background(), fixed(spotify, nil), fixed(nil, nil)); got != nil {
		t.Fatalf("stopped YouTube fell back to Spotify: %+v", got)
	}
	paused := &Info{Source: "youtube_music", Status: Paused}
	if got := tracker.Pick(context.Background(), fixed(spotify, nil), fixed(paused, nil)); got != paused {
		t.Fatalf("got %+v", got)
	}
}
