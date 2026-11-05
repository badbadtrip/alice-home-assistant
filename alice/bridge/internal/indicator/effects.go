package indicator

import (
	"math"
	"strconv"
	"time"
)

// Pixel is one LED in display order.
type Pixel struct{ R, G, B uint8 }

// Light is the exact RGB value before it is reduced to 8 bits.
type Light [3]float64

// outputCap keeps the LEDs below full drive, which they cannot sustain for hours.
const outputCap = .8

func hue(h float64) [3]float64 {
	h = math.Mod(h, 1) * 6
	x := 1 - math.Abs(math.Mod(h, 2)-1)
	switch int(h) {
	case 0:
		return [3]float64{1, x, 0}
	case 1:
		return [3]float64{x, 1, 0}
	case 2:
		return [3]float64{0, 1, x}
	case 3:
		return [3]float64{0, x, 1}
	case 4:
		return [3]float64{x, 0, 1}
	}
	return [3]float64{1, 0, x}
}

// pixel applies gamma 2, so brightness steps look even to the eye.
func light(c [3]float64, level float64) Light {
	var out Light
	for i, v := range c {
		v = math.Max(0, math.Min(1, v*level))
		out[i] = v * v * outputCap * 255
	}
	return out
}

// smoother turns exact light into 8-bit frames without visible steps. A short
// low-pass softens every change and crossfades between looks; carrying each
// rounding error into the next frame shows values between the 8-bit levels.
type smoother struct {
	now, carry []Light
}

const smoothing = .25

func (s *smoother) next(target []Light) []Pixel {
	if s.now == nil {
		s.now, s.carry = make([]Light, len(target)), make([]Light, len(target))
	}
	frame := make([]Pixel, len(target))
	for i := range target {
		var out [3]uint8
		for c := range 3 {
			s.now[i][c] += (target[i][c] - s.now[i][c]) * smoothing
			wanted := s.now[i][c] + s.carry[i][c]
			shown := math.Max(0, math.Min(255, math.Round(wanted)))
			s.carry[i][c] = wanted - shown
			out[c] = uint8(shown)
		}
		frame[i] = Pixel{out[0], out[1], out[2]}
	}
	return frame
}

func (s *smoother) dark() bool {
	for _, p := range s.now {
		if p[0]+p[1]+p[2] > .05 {
			return false
		}
	}
	return true
}

// noise is a stable pseudo-random value in 0..1 for an LED at a moment.
func noise(index, step int) float64 {
	x := uint32(index)*374761393 + uint32(step)*668265263
	x = (x ^ (x >> 13)) * 1274126177
	return float64((x^(x>>16))&0xffff) / 65535
}

// drift moves smoothly between random values, so nothing ever jumps.
func drift(index int, t float64) float64 {
	step := math.Floor(t)
	blend := t - step
	blend = blend * blend * (3 - 2*blend)
	return noise(index, int(step))*(1-blend) + noise(index, int(step)+1)*blend
}

// Render draws one frame for a ring of n LEDs.
func (c Config) Render(elapsed time.Duration, n int) []Light {
	period := c.PeriodMS
	if period == 0 {
		period = 2400
	}
	ms := float64(elapsed) / float64(time.Millisecond)
	phase := math.Mod(ms, float64(period)) / float64(period)
	rgb, _ := strconv.ParseUint(c.Color[1:], 16, 32)
	color := [3]float64{float64(rgb>>16) / 255, float64((rgb>>8)&255) / 255, float64(rgb&255) / 255}
	brightness := 1.0
	if c.Brightness != nil {
		brightness = float64(*c.Brightness) / 100
	}
	frame := make([]Light, n)
	for i := range frame {
		position := float64(i) / float64(n)
		shade, level := color, brightness
		switch c.Mode {
		case "rainbow":
			shade = hue(phase + position)
		case "comet":
			behind := math.Mod(phase-position+1, 1)
			// The LED ahead of the head fades in, so the head never jumps onto it.
			lead := 1.5 / float64(n)
			if behind > 1-lead {
				level *= math.Max(.03, (behind-1+lead)/lead)
			} else {
				level *= math.Max(.03, math.Exp(-behind*5))
			}
		case "wave":
			level *= .55 + .45*math.Sin(2*math.Pi*(phase*2-position))
		case "sparkle":
			beat := ms/float64(period)*(.7+.6*noise(i, 1)) + noise(i, 2)
			glint := math.Max(0, math.Sin(2*math.Pi*beat))
			level *= .12 + .88*glint*glint*glint
		case "fire":
			t := ms / float64(period) * 8
			flame := .3 + .7*(.6*drift(i, t)+.4*drift(i+64, t*2.3))
			shade = [3]float64{1, .08 + .42*flame*flame, 0}
			level *= flame
		default:
			level = float64(c.Level(elapsed)) / 100
		}
		frame[i] = light(shade, level)
	}
	return frame
}
