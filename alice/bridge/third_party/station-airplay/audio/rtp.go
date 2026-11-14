// Adapted from goplay2; validates the buffered AirPlay packet before authentication.
package audio

import (
	"encoding/binary"
	"fmt"
	"github.com/skrashevich/station-airplay/codec"
	"golang.org/x/crypto/chacha20poly1305"
	"io"
)

type TcpPacket struct {
	SequenceNumber uint32
	Timestamp      uint32
}
type PCMFrame struct {
	TcpPacket
	pcmData     []byte
	encodedData []byte
}

func (p *PCMFrame) Data() []byte { return p.pcmData }
func ReadPacket(r io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n < 38 {
		return nil, fmt.Errorf("invalid buffered RTP length %d", n)
	}
	b := make([]byte, n-2)
	_, err := io.ReadFull(r, b)
	return b, err
}
func ParseFrame(key, raw []byte) (*PCMFrame, error) {
	if len(raw) < 36 || raw[0] != 0x80 {
		return nil, fmt.Errorf("invalid buffered RTP header/length")
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	copy(nonce[4:], raw[len(raw)-8:])
	cleartext, err := aead.Open(nil, nonce, raw[12:len(raw)-8], raw[4:12])
	if err != nil {
		return nil, fmt.Errorf("RTP authentication failed: %w", err)
	}
	seq := uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	return &PCMFrame{TcpPacket: TcpPacket{seq, binary.BigEndian.Uint32(raw[4:8])}, encodedData: cleartext}, nil
}
func NewFrame(d *codec.AacDecoder, key, raw []byte) (*PCMFrame, error) {
	frame, err := ParseFrame(key, raw)
	if err != nil {
		return nil, err
	}
	frame.pcmData, err = d.Decode(frame.encodedData)
	return frame, err
}
