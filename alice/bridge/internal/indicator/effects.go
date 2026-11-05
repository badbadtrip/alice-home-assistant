package indicator

import (
	"math"
	"strconv"
	"time"
)

// Pixel is one LED in display order.
type Pixel struct{ R, G, B uint8 }

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
func pixel(c [3]float64, level float64) Pixel {
	channel := func(v float64) uint8 {
		v = math.Max(0, math.Min(1, v*level))
		return uint8(math.Round(v * v * outputCap * 255))
	}
	return Pixel{channel(c[0]), channel(c[1]), channel(c[2])}
}

// noise is a stable pseudo-random value in 0..1 for an LED at a moment.
func noise(index, step int) float64 {
	x := uint32(index)*374761393 + uint32(step)*668265263
	x = (x ^ (x >> 13)) * 1274126177
	return float64((x^(x>>16))&0xffff) / 65535
}

// Render draws one frame for a ring of n LEDs.
func (c Config) Render(elapsed time.Duration, n int) []Pixel {
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
	frame := make([]Pixel, n)
	for i := range frame {
		position := float64(i) / float64(n)
		shade, level := color, brightness
		switch c.Mode {
		case "rainbow":
			shade = hue(phase + position)
		case "comet":
			behind := math.Mod(phase-position+1, 1)
			level *= math.Max(.04, math.Pow(1-behind, 3))
		case "wave":
			level *= .55 + .45*math.Sin(2*math.Pi*(phase*2-position))
		case "sparkle":
			step := int(ms / 90)
			level *= .18
			if noise(i, step/3) > .72 {
				level = brightness
			}
		case "fire":
			step := int(ms / 70)
			flame := .35 + .65*(noise(i, step)+noise(i, step/2))/2
			shade = [3]float64{1, .08 + .42*flame*flame, 0}
			level *= flame
		default:
			level = float64(c.Level(elapsed)) / 100
		}
		frame[i] = pixel(shade, level)
	}
	return frame
}
