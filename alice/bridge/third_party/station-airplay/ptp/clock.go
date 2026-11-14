// Adapted from goplay2; system time is untouched.
package ptp

import (
	"sync"
	"time"
)

const maxTimelines = 16

type VirtualClock struct {
	base            time.Time
	delay           time.Duration
	mu              sync.RWMutex
	offset          time.Duration
	current, pinned uint64
	timelines       map[uint64]time.Duration
	order           []uint64
}

func NewVirtualClock(delay int64) *VirtualClock {
	return &VirtualClock{base: time.Now(), delay: time.Duration(delay) * time.Millisecond}
}
func (v *VirtualClock) Offset(diff time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.offset += diff
	for id, offset := range v.timelines {
		v.timelines[id] = offset + diff
	}
}
func (v *VirtualClock) Now() time.Time { return v.At(time.Now()) }
func (v *VirtualClock) At(stamp time.Time) time.Time {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return time.Unix(0, int64(stamp.Sub(v.base)-v.delay-v.offset))
}

// NowForTimeline preserves anchors in earlier grandmaster domains. Unknown
// nonzero timelines must converge before audio can use their timestamps.
func (v *VirtualClock) NowForTimeline(id uint64) (time.Time, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	offset := v.offset
	if id != 0 {
		var ok bool
		offset, ok = v.timelines[id]
		if !ok {
			return time.Time{}, false
		}
	}
	return time.Unix(0, int64(time.Since(v.base)-v.delay-offset)), true
}

// OffsetForTimeline applies a measured local-minus-master offset. A new GM's
// epoch changes the raw clock, while previous timelines keep their mapping.
// Later corrections of that GM refine all retained mappings together.
func (v *VirtualClock) OffsetForTimeline(id uint64, diff time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.offset += diff
	if id == v.current {
		for timeline, offset := range v.timelines {
			v.timelines[timeline] = offset + diff
		}
	}
	v.current = id
	if id == 0 {
		return
	}
	if v.timelines == nil {
		v.timelines = make(map[uint64]time.Duration)
	}
	if _, ok := v.timelines[id]; !ok {
		if len(v.timelines) == maxTimelines {
			for i, old := range v.order {
				if old != v.pinned {
					delete(v.timelines, old)
					v.order = append(v.order[:i], v.order[i+1:]...)
					break
				}
			}
		}
		v.order = append(v.order, id)
	}
	v.timelines[id] = v.offset
}
func (v *VirtualClock) pinTimeline(id uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pinned = id
}
func (v *VirtualClock) resetTimelines() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.timelines)
	v.order = nil
	v.current = 0
	v.pinned = 0
}
