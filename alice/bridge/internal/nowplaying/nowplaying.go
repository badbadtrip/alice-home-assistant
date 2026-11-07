// Package nowplaying describes what a speaker is playing in one form for every source.
package nowplaying

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// budget keeps /healthz well inside the 3 seconds the manager waits for it. A source
// that does not answer in time is left out; health must not fail because of it.
const budget = 1500 * time.Millisecond

const (
	Playing   = "playing"
	Paused    = "paused"
	Buffering = "buffering"
)

// Info is empty in the fields a source cannot tell: AirPlay gives only Source and Status.
type Info struct {
	Source     string `json:"source"`
	Status     string `json:"status"`
	Title      string `json:"title,omitzero"`
	Artist     string `json:"artist,omitzero"`
	Album      string `json:"album,omitzero"`
	Cover      string `json:"cover,omitzero"`
	PositionMS int64  `json:"position_ms,omitzero"`
	DurationMS int64  `json:"duration_ms,omitzero"`
}

// Source returns nil when it holds nothing for this speaker.
type Source func(context.Context) (*Info, error)

// Pick asks every source at once. The first in order with sound wins; otherwise the
// first one holding a paused item.
func Pick(ctx context.Context, sources ...Source) *Info {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	found := make([]*Info, len(sources))
	var group sync.WaitGroup
	for i, source := range sources {
		group.Go(func() {
			if info, err := source(ctx); err == nil {
				found[i] = info
			}
		})
	}
	group.Wait()
	var held *Info
	for _, info := range found {
		if info == nil {
			continue
		}
		if info.Status != Paused {
			return info
		}
		if held == nil {
			held = info
		}
	}
	return held
}

// Tracker remembers which source played last. A paused item belongs on the card only
// when it comes from that source: Spotify paused hours ago must not replace YouTube.
type Tracker struct {
	mu   sync.Mutex
	last string
}

func (t *Tracker) Pick(ctx context.Context, sources ...Source) *Info {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	found := make([]*Info, len(sources))
	var group sync.WaitGroup
	for i, source := range sources {
		group.Go(func() {
			if info, err := source(ctx); err == nil {
				found[i] = info
			}
		})
	}
	group.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, info := range found {
		if info != nil && info.Status != Paused {
			t.last = info.Source
			return info
		}
	}
	for _, info := range found {
		if info != nil && info.Source == t.last {
			return info
		}
	}
	return nil
}

// Watch looks at the players every second and reports a change at once. The same
// state is sent again every 30 seconds, so the server never keeps a stale track for long.
func Watch(ctx context.Context, tracker *Tracker, sources func() []Source, report func(context.Context, *Info) error) {
	var last []byte
	var sent time.Time
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		info := tracker.Pick(ctx, sources()...)
		if data, _ := json.Marshal(info); string(data) != string(last) || time.Since(sent) > 30*time.Second {
			if report(ctx, info) == nil {
				last, sent = data, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
