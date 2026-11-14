package audio

import (
	"context"
	"sync"
)

type Buffer struct {
	mu       sync.Mutex
	frames   []*PCMFrame
	capacity int
	changed  chan struct{}
}

func NewBuffer(capacity int) *Buffer {
	return &Buffer{capacity: capacity, changed: make(chan struct{})}
}
func (b *Buffer) notify()                  { close(b.changed); b.changed = make(chan struct{}) } // caller holds mu
func (b *Buffer) Changed() <-chan struct{} { b.mu.Lock(); defer b.mu.Unlock(); return b.changed }
func (b *Buffer) TryPush(f *PCMFrame) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.frames) >= b.capacity {
		return false
	}
	b.frames = append(b.frames, f)
	b.notify()
	return true
}
func (b *Buffer) Push(ctx context.Context, f *PCMFrame) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed := b.Changed()
		if b.TryPush(f) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (b *Buffer) Len() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.frames) }
func (b *Buffer) Peek() *PCMFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.frames) == 0 {
		return nil
	}
	return b.frames[0]
}
func (b *Buffer) Pop() *PCMFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.frames) == 0 {
		return nil
	}
	f := b.frames[0]
	copy(b.frames, b.frames[1:])
	b.frames[len(b.frames)-1] = nil
	b.frames = b.frames[:len(b.frames)-1]
	b.notify()
	return f
}
func sequenceInRange(seq, from, until uint32) bool {
	return (seq-from)&0xffffff <= (until-from)&0xffffff
}
func sequenceAfter(seq, other uint32) bool { return int32((seq-other)<<8) > 0 }
func (b *Buffer) filter(keep func(*PCMFrame) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.frames[:0]
	for _, f := range b.frames {
		if keep(f) {
			out = append(out, f)
		}
	}
	clear(b.frames[len(out):])
	b.frames = out
	b.notify()
}
func (b *Buffer) Flush(from, until uint32) {
	b.filter(func(f *PCMFrame) bool { return !sequenceInRange(f.SequenceNumber, from, until) })
}
func (b *Buffer) TrimBefore(rtp uint32) {
	b.filter(func(f *PCMFrame) bool { return int32(f.Timestamp-rtp) >= 0 })
}

// tryPushOrdered is the nonblocking UDP queue. Timestamps are compared modulo
// the RTP clock; the queue spans seconds, far less than half its 32-bit range.
func (b *Buffer) tryPushOrdered(f *PCMFrame) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	index := len(b.frames)
	for i, old := range b.frames {
		if f.Timestamp == old.Timestamp || f.SequenceNumber == old.SequenceNumber {
			return false
		}
		if index == len(b.frames) && int32(f.Timestamp-old.Timestamp) < 0 {
			index = i
		}
	}
	if len(b.frames) >= b.capacity {
		if index == len(b.frames) {
			return false
		}
		b.frames[len(b.frames)-1] = nil
		b.frames = b.frames[:len(b.frames)-1]
	}
	b.frames = append(b.frames, nil)
	copy(b.frames[index+1:], b.frames[index:])
	b.frames[index] = f
	b.notify()
	return true
}

// clearAll returns the newest queued timestamp before releasing its frames.
// Realtime callers use it to reject known stale packets after headerless FLUSH.
func (b *Buffer) clearAll() (newest uint32, known bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.frames) > 0 {
		newest = b.frames[len(b.frames)-1].Timestamp
		known = true
	}
	clear(b.frames)
	b.frames = b.frames[:0]
	b.notify()
	return newest, known
}
