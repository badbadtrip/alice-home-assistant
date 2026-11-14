// ALAC adapter for the negotiated AirPlay 2 realtime stereo profile. No Cgo.
package codec

import (
	"fmt"

	alac "github.com/mycophonic/saprobe-alac"
)

type AlacDecoder struct {
	decoder *alac.PacketDecoder
}

// NewAlacDecoder configures the fixed type-96 ALAC profile advertised by this
// receiver: signed 16-bit stereo, 44100 Hz, at most 352 samples per packet.
func NewAlacDecoder() (*AlacDecoder, error) {
	decoder, err := alac.NewPacketDecoder(alac.PacketConfig{
		FrameLength: 352,
		BitDepth:    16,
		NumChannels: 2,
		PB:          40,
		MB:          10,
		KB:          14,
		MaxRun:      255,
		SampleRate:  44100,
	})
	if err != nil {
		return nil, fmt.Errorf("configure ALAC: %w", err)
	}
	return &AlacDecoder{decoder: decoder}, nil
}

func (d *AlacDecoder) Decode(data []byte) (pcm []byte, err error) {
	if d.decoder == nil {
		return nil, fmt.Errorf("ALAC is not configured")
	}
	// Network input must never panic through the receiver's control services.
	defer func() {
		if recover() != nil {
			pcm = nil
			err = fmt.Errorf("invalid ALAC frame")
		}
	}()
	// DecodePacket allocates a separate PCM buffer for every call. The media
	// player may apply gain in place without mutating the decoder's storage.
	pcm, err = d.decoder.DecodePacket(data)
	if err != nil {
		return nil, fmt.Errorf("decode ALAC: %w", err)
	}
	if len(pcm) == 0 || len(pcm) > 352*4 || len(pcm)%4 != 0 {
		return nil, fmt.Errorf("invalid ALAC output byte count: %d", len(pcm))
	}
	return pcm, nil
}

func (d *AlacDecoder) SampleRate() int { return 44100 }
