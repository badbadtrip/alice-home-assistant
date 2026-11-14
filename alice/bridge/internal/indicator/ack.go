package indicator

import (
	"math"
	"time"
)

// AckDuration is how long a silent confirmation (10.4) shows on the ring.
const AckDuration = 700 * time.Millisecond

// Ack shows a short sign for a player command that needs no words: pause and
// resume pulse the whole ring, next sweeps clockwise, previous the other way.
func (c *Controller) Ack(action string) {
	c.ackKind.Store(action)
	c.ackAt.Store(time.Now().UnixNano())
	c.poke()
}

// ack returns the confirmation frame while one is showing.
func (c *Controller) ack(now time.Time, size int) ([]Light, bool) {
	since := now.Sub(time.Unix(0, c.ackAt.Load()))
	kind, _ := c.ackKind.Load().(string)
	if kind == "" || since < 0 || since >= AckDuration {
		return nil, false
	}
	return ackFrame(kind, float64(since)/float64(AckDuration), size), true
}

func ackFrame(kind string, t float64, size int) []Light {
	white := [3]float64{1, 1, 1}
	fade := math.Sin(math.Pi * t) // up and down once
	out := make([]Light, size)
	switch kind {
	case "next", "previous":
		head := t * float64(size)
		for i := range out {
			pos := float64(i)
			if kind == "previous" {
				pos = float64(size - 1 - i)
			}
			d := head - pos
			if d < 0 || d > 4 {
				continue
			}
			out[i] = light(white, (1-d/4)*fade+.15)
		}
	default:
		level := .6 * fade
		if kind == "pause" {
			level = .6 * (1 - t) // fades out: music stopped
		}
		for i := range out {
			out[i] = light(white, level)
		}
	}
	return out
}
