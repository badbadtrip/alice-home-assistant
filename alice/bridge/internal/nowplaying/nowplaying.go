// Package nowplaying describes what a speaker is playing in one form for every source.
package nowplaying

import (
	"context"
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
