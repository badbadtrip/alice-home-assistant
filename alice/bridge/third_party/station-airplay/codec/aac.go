// AAC adapter replacing goplay2's libfdk-aac binding. No cgo.
package codec

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/skrashevich/go-aac/pkg/decoder"
)

type AacDecoder struct {
	decoder *decoder.Decoder
	rate    int
}

func NewAacDecoder() *AacDecoder { return &AacDecoder{} }
func (d *AacDecoder) InitRaw(asc []byte) error {
	if len(asc) != 2 {
		return fmt.Errorf("AAC requires a two-byte AudioSpecificConfig")
	}
	dec := decoder.New()
	if err := dec.SetASC(asc); err != nil {
		return err
	}
	if dec.Config.Profile != 2 || dec.Config.ChanConfig != 2 || (dec.Config.SampleRate != 44100 && dec.Config.SampleRate != 48000) {
		return fmt.Errorf("only AAC-LC stereo at 44100/48000 Hz is supported")
	}
	d.decoder = dec
	d.rate = dec.Config.SampleRate
	return nil
}
func (d *AacDecoder) Decode(data []byte) (pcm []byte, err error) {
	if d.decoder == nil {
		return nil, fmt.Errorf("AAC is not configured")
	}
	// A malformed network frame must not take down discovery/control services.
	defer func() {
		if v := recover(); v != nil {
			pcm = nil
			err = fmt.Errorf("invalid AAC frame: %v", v)
		}
	}()
	samples, err := d.decoder.DecodeFrame(data)
	if err != nil {
		return nil, err
	}
	if len(samples) != 2048 {
		return nil, fmt.Errorf("invalid AAC output sample count: %d", len(samples))
	}
	pcm = make([]byte, len(samples)*2)
	for i, v := range samples {
		x := float64(v) * 32768
		if math.IsNaN(x) {
			x = 0
		}
		x = min(32767, max(-32768, x))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(math.Round(x))))
	}
	return pcm, nil
}
func (d *AacDecoder) SampleRate() int { return d.rate }

func LinearVolume(db float64) float64 {
	if math.IsNaN(db) || math.IsInf(db, 0) || db <= -144 {
		return 0
	}
	return math.Pow(10, min(0, db)/20)
}
func ApplyGain(samples []int16, gain float64) {
	if math.IsNaN(gain) || math.IsInf(gain, 0) {
		gain = 0
	}
	gain = min(1, max(0, gain))
	for i, s := range samples {
		samples[i] = int16(math.Round(float64(s) * gain))
	}
}
