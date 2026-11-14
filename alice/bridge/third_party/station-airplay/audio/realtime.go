// AirPlay 2 realtime RTP/ALAC uses UDP media and a separate PTP sync socket.
package audio

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/skrashevich/station-airplay/codec"
	"golang.org/x/crypto/chacha20poly1305"
)

const realtimePacketLimit = 4096
const realtimeBufferFrames = 512   // > 4 seconds at 352 frames/packet, 44.1 kHz.
const realtimeAnchorOffset = 11025 // AirPlay realtime's additional 250 ms.

type realtimeState struct {
	data, control   *net.UDPConn
	cipher          cipher.AEAD
	remoteControl   *net.UDPAddr
	workers         sync.WaitGroup
	haveFloor       bool
	floor           uint32 // latest packet committed to decoding/playout, not latest arrival
	haveSequence    bool
	sequence        uint16
	requestSequence uint16
	nextRequest     time.Time
	nextErrorLog    time.Time
}

// SetupRealtime accepts only the negotiated ALAC/S16/stereo/44.1 kHz profile.
// The caller binds the stream to the authenticated control connection's PeerIP.
func (s *Server) SetupRealtime(key []byte, rate int) (dataPort, controlPort int, failure error) {
	if len(key) != 32 || rate != 44100 {
		return 0, 0, fmt.Errorf("unsupported realtime audio key/format")
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return 0, 0, err
	}
	if _, err := codec.NewAlacDecoder(); err != nil {
		return 0, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return 0, 0, err
	}
	if s.started {
		return 0, 0, fmt.Errorf("stream already set up")
	}
	if s.PeerIP == nil {
		return 0, 0, fmt.Errorf("realtime audio requires an authenticated peer")
	}
	if err := s.player.claim(s); err != nil {
		return 0, 0, err
	}
	defer func() {
		if failure != nil {
			s.player.release(s)
		}
	}()
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(s.player.BindIP, "0"))
	if err != nil {
		return 0, 0, err
	}
	data, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return 0, 0, err
	}
	control, err := net.ListenUDP("udp4", addr)
	if err != nil {
		data.Close()
		return 0, 0, err
	}
	data.SetReadBuffer(1 << 20)
	s.rate = rate
	s.sharedKey = append([]byte(nil), key...)
	s.buffer = NewBuffer(realtimeBufferFrames)
	state := &realtimeState{data: data, control: control, cipher: aead}
	s.realtime = state
	s.started = true
	state.workers.Go(func() { s.receiveRealtime(data, false) })
	state.workers.Go(func() { s.receiveRealtime(control, true) })
	go func() { state.workers.Wait(); close(s.inputDone) }()
	go s.play()
	return data.LocalAddr().(*net.UDPAddr).Port, control.LocalAddr().(*net.UDPAddr).Port, nil
}

// SetRealtimeControlPort supplies the sender's advertised port for best-effort
// retransmission requests. A valid sync datagram also supplies this endpoint.
func (s *Server) SetRealtimeControlPort(port uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.realtime != nil && port != 0 {
		s.realtime.remoteControl = &net.UDPAddr{IP: append(net.IP(nil), s.PeerIP...), Port: int(port)}
	}
}

func ParseRealtimeFrame(key, raw []byte) (*PCMFrame, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return parseRealtimeFrame(aead, raw)
}
func parseRealtimeFrame(aead cipher.AEAD, raw []byte) (*PCMFrame, error) {
	if len(raw) < 37 || len(raw) > realtimePacketLimit || raw[0] != 0x80 || raw[1]&0x7f != 96 {
		return nil, fmt.Errorf("invalid realtime RTP header/length")
	}
	var nonce [12]byte
	copy(nonce[4:], raw[len(raw)-8:])
	payload, err := aead.Open(nil, nonce[:], raw[12:len(raw)-8], raw[4:12])
	if err != nil {
		return nil, fmt.Errorf("realtime RTP authentication failed")
	}
	return &PCMFrame{TcpPacket: TcpPacket{SequenceNumber: uint32(binary.BigEndian.Uint16(raw[2:4])), Timestamp: binary.BigEndian.Uint32(raw[4:8])}, encodedData: payload}, nil
}

func parseRealtimeSync(raw []byte) (uint32, time.Time, uint64, error) {
	if len(raw) != 28 || raw[0]>>6 != 2 || raw[1] != 0xd7 {
		return 0, time.Time{}, 0, fmt.Errorf("invalid realtime sync header/length")
	}
	ns := binary.BigEndian.Uint64(raw[8:16])
	if ns > math.MaxInt64 {
		return 0, time.Time{}, 0, fmt.Errorf("invalid realtime sync timestamp")
	}
	rtp := binary.BigEndian.Uint32(raw[4:8])
	current := binary.BigEndian.Uint32(raw[16:20])
	// A sync describes the same short realtime window, modulo the RTP clock.
	if delta := int32(current - rtp); delta < 0 || delta > 44100*5 {
		return 0, time.Time{}, 0, fmt.Errorf("invalid realtime sync latency")
	}
	return rtp - realtimeAnchorOffset, time.Unix(0, int64(ns)), binary.BigEndian.Uint64(raw[20:28]), nil
}

func (s *Server) receiveRealtime(conn *net.UDPConn, control bool) {
	state := s.realtime
	buffer := make([]byte, realtimePacketLimit+1)
	for {
		n, peer, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if s.ctx.Err() == nil {
				s.Close()
			}
			return
		}
		if !peer.IP.Equal(s.PeerIP) || n > realtimePacketLimit {
			continue
		}
		raw := buffer[:n]
		if control {
			if n >= 2 && raw[1] == 0xd7 {
				rtp, network, clock, err := parseRealtimeSync(raw)
				if err != nil {
					s.logRealtimeError(err)
					continue
				}
				s.mu.Lock()
				state.remoteControl = &net.UDPAddr{IP: append(net.IP(nil), peer.IP...), Port: peer.Port}
				s.mu.Unlock()
				s.player.SetTimingPeers(s, []net.IP{s.PeerIP}, clock)
				s.applyRealtimeSync(rtp, network, clock)
				continue
			}
			// Retransmission replies carry a four-byte control header before RTP.
			if n < 4 || raw[0]>>6 != 2 || raw[1] != 0xd6 {
				continue
			}
			raw = raw[4:]
		}
		frame, err := parseRealtimeFrame(state.cipher, raw)
		if err != nil {
			s.logRealtimeError(err)
			continue
		}
		s.enqueueRealtime(frame)
	}
}

func (s *Server) logRealtimeError(err error) {
	s.mu.Lock()
	now := time.Now()
	log := s.realtime != nil && !now.Before(s.realtime.nextErrorLog)
	if log {
		s.realtime.nextErrorLog = now.Add(5 * time.Second)
	}
	s.mu.Unlock()
	if log {
		s.player.Logf("realtime audio input: %v", err)
	}
}

func (s *Server) enqueueRealtime(f *PCMFrame) bool {
	s.mu.Lock()
	state := s.realtime
	if state == nil || s.ctx.Err() != nil || (state.haveFloor && int32(f.Timestamp-state.floor) <= 0) || (s.haveAnchor && int32(f.Timestamp-s.anchorRTP) < 0) {
		s.mu.Unlock()
		return false
	}
	seq := uint16(f.SequenceNumber)
	var request []byte
	var remote *net.UDPAddr
	// Request small gaps once while retaining later frames for their deadlines.
	// If recovery fails, playout inserts bounded silence instead of waiting.
	if state.haveSequence {
		missing := int16(seq-state.sequence) - 1
		if missing > 0 && missing <= 32 && state.remoteControl != nil && !time.Now().Before(state.nextRequest) {
			request = make([]byte, 8)
			request[0], request[1] = 0x80, 0xd5
			state.requestSequence++
			binary.BigEndian.PutUint16(request[2:], state.requestSequence)
			binary.BigEndian.PutUint16(request[4:], state.sequence+1)
			binary.BigEndian.PutUint16(request[6:], uint16(missing))
			remote = state.remoteControl
			state.nextRequest = time.Now().Add(20 * time.Millisecond)
		}
	}
	if !state.haveSequence || int16(seq-state.sequence) > 0 {
		state.sequence = seq
		state.haveSequence = true
	}
	ok := s.buffer.tryPushOrdered(f)
	s.mu.Unlock()
	if request != nil {
		state.control.WriteToUDP(request, remote)
	}
	return ok
}

// FlushRealtime clears queued and in-flight PCM, rejecting packets from the
// discarded RTP timeline even when late UDP packets arrive after FLUSH.
func (s *Server) FlushRealtime(rtp uint32) {
	s.mu.Lock()
	s.generation++
	s.buffer.TrimBefore(rtp)
	if s.realtime != nil {
		s.realtime.floor = rtp - 1
		s.realtime.haveFloor = true
		s.realtime.haveSequence = false
	}
	s.anchorRTP = rtp
	s.haveAnchor = false
	s.playing = false
	old := s.output
	s.output = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s.signal()
}

// Periodic realtime sync updates the moving clock mapping. Unlike a seek, a
// normal sync must not restart the ALSA output or discard decoder progress.
func (s *Server) applyRealtimeSync(rtp uint32, network time.Time, timeline uint64) {
	s.mu.Lock()
	starting := !s.playing
	if starting {
		s.generation++
	}
	s.anchorRTP = rtp
	s.anchorTime = network
	s.anchorClockID = timeline
	s.haveAnchor = true
	s.playing = true
	s.buffer.TrimBefore(rtp)
	s.mu.Unlock()
	if starting {
		s.player.Logf("realtime audio anchor rtp=%d seconds=%d", rtp, network.Unix())
	}
	s.signal()
}

func (s *Server) IsRealtime() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.realtime != nil
}

// FlushRealtimeAll handles a sender's headerless FLUSH without inventing an
// RTP timestamp. Preserve only a known queue/committed floor, including wrap.
func (s *Server) FlushRealtimeAll() {
	s.mu.Lock()
	s.generation++
	newest, known := s.buffer.clearAll()
	if state := s.realtime; state != nil {
		if known && (!state.haveFloor || int32(newest-state.floor) > 0) {
			state.floor = newest
			state.haveFloor = true
		}
		state.haveSequence = false
	}
	s.haveAnchor = false
	s.playing = false
	old := s.output
	s.output = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s.signal()
}
