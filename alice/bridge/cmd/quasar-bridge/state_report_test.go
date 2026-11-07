package main

import (
	"context"
	"testing"
	"time"
)

func TestWatchStateReportsChangesNotCounters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	tick := 0
	health := func(context.Context) map[string]any {
		tick++
		revision := "a"
		if tick >= 3 {
			revision = "b"
		}
		return map[string]any{"status": "ok", "client_frames": tick, "voice_phrase_revision": revision}
	}
	reports := 0
	watchState(ctx, health, func(_ context.Context, payload map[string]any) error {
		if payload["health"] == nil || payload["system"] == nil {
			t.Fatal("payload misses health or system")
		}
		reports++
		return nil
	})
	if reports != 2 {
		t.Fatalf("got %d reports, want the first state and the revision change", reports)
	}
}
