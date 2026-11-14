// Adapted from goplay2; bounded parsing and cancellation added.
package ptp

import (
	"context"
	"encoding"
	"encoding/binary"
	"fmt"
	wire "github.com/albanseurat/go-ptp"
	"log"
	"maps"
	"net"
	"sync"
	"time"
)

type serverMsg struct {
	LocalTimeStamp time.Time
	Msg            encoding.BinaryUnmarshaler
	Src            *net.UDPAddr
	ClockIdentity  uint64
}
type Server struct {
	Logger                *log.Logger
	genConn, eventConn    *net.UDPConn
	clock                 *VirtualClock
	identity              uint64
	measurements          *measures
	mu                    sync.Mutex
	owner                 any
	allowed               map[string]bool
	master                uint64
	generation            uint64
	sourceID              uint64
	sourceIP              string
	sourcePort            uint16
	grandmaster           uint64
	measurementGeneration uint64
	hasTiming             bool
	transitioning         bool
	haveCandidate         bool
	candidateOffset       time.Duration
	grandmasterChangedAt  time.Time
}

func Listen(clock *VirtualClock, ip net.IP, identity uint64) (*Server, error) {
	g, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 320})
	if err != nil {
		return nil, fmt.Errorf("PTP port 320: %w", err)
	}
	e, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 319})
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("PTP port 319: %w", err)
	}
	return &Server{genConn: g, eventConn: e, clock: clock, identity: identity, measurements: newMeasurements()}, nil
}
func (s *Server) Close() { s.genConn.Close(); s.eventConn.Close() }
func (s *Server) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	failures := make(chan error, 2)
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Close)
	defer stop()
	packets := make(chan serverMsg, 32)
	var wg sync.WaitGroup
	for _, conn := range []*net.UDPConn{s.genConn, s.eventConn} {
		wg.Go(func() {
			var b [2048]byte
			for {
				n, src, err := conn.ReadFromUDP(b[:])
				if err != nil {
					failures <- err
					return
				}
				stamp := time.Now()
				msg, err := decodePacket(b[:n])
				if err != nil || msg == nil {
					continue
				}
				select {
				case packets <- serverMsg{LocalTimeStamp: stamp, Msg: msg, Src: src, ClockIdentity: binary.BigEndian.Uint64(b[20:28])}:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	defer func() { cancel(); s.Close(); wg.Wait() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failures:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("PTP receive: %w", err)
		case p := <-packets:
			s.processPacket(p)
		}
	}
}

func (s *Server) processPacket(p serverMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.acceptPacket(p) {
		return
	}
	if s.measurementGeneration != s.generation {
		s.measurements = newMeasurements()
		s.measurementGeneration = s.generation
	}
	switch x := p.Msg.(type) {
	case *wire.AnnounceMsg:
		s.measurements.currentUTCoffset = time.Duration(x.CurrentUtcOffset) * time.Second
	case *wire.SyncMsg:
		// Correct both parts of the two-step forward exchange. The wire library
		// exposes the signed upper 48 bits as unsigned, so sign-extend explicitly.
		s.measurements.addSync(x.SequenceID, s.clock.At(p.LocalTimeStamp).Add(-correction(x.Header)))
	case *wire.FollowUpMsg:
		s.measurements.addFollowUp(x.SequenceID, x.PreciseOriginTimestamp.Add(correction(x.Header)))
		request := &wire.DelReqMsg{Header: wire.Header{Flags: wire.Flags{Unicast: true, TwoSteps: true, TimeScale: true}, MessageType: wire.DelayReqMsgType, MessageLength: wire.HeaderLen + wire.DelayReqPayloadLen, VersionPTP: wire.Version2, ClockIdentity: s.identity, PortNumber: 1, SequenceID: x.SequenceID, LogMessagePeriod: 0x7f}, OriginTimestamp: time.Unix(0, 0)}
		b, err := request.MarshalBinary()
		if err != nil {
			return
		}
		b[0] |= 0x10
		s.measurements.addDelayReq(x.SequenceID, s.clock.Now())
		target := *p.Src
		target.Port = 319
		s.eventConn.WriteToUDP(b, &target)
	case *wire.DelRespMsg:
		if x.RequestingPortIdentity != s.identity || x.RequestingPortID != 1 {
			return
		}
		// Apple's relay can repeat FollowUp's origin here; correctionField
		// advances that origin to the DelayReq reception time.
		s.measurements.addDelayResp(x.SequenceID, x.ReceiveTimestamp.Add(correction(x.Header)))
		if result, err := s.measurements.latest(); err == nil {
			// Sync and Announce use separate UDP ports. Never let a new epoch
			// rewrite the old GM's timeline before its Announce arrives.
			if s.hasTiming && !s.transitioning && (result.Offset > 5*time.Second || result.Offset < -5*time.Second) {
				return
			}
			// After a GM change, a complete exchange from the old epoch may still
			// arrive. Require two consistent samples before publishing the mapping.
			if s.transitioning {
				if p.LocalTimeStamp.Sub(s.grandmasterChangedAt) < 400*time.Millisecond {
					return
				}
				difference := result.Offset - s.candidateOffset
				if !s.haveCandidate || difference > 100*time.Millisecond || difference < -100*time.Millisecond {
					s.candidateOffset = result.Offset
					s.haveCandidate = true
					return
				}
				s.transitioning = false
				s.haveCandidate = false
			}
			s.hasTiming = true
			if result.Offset > 100*time.Millisecond || result.Offset < -100*time.Millisecond {
				s.logf("PTP correction gm=%016x offset=%v delay=%v", s.grandmaster, result.Offset, result.Delay)
			}
			id := s.grandmaster
			if id == 0 {
				id = s.sourceID
			}
			s.clock.OffsetForTimeline(id, result.Offset)
		}
	}
	if len(s.measurements.serverToClient) > 64 || len(s.measurements.clientToServer) > 64 {
		s.measurements = newMeasurements()
	}
}

func correction(h wire.Header) time.Duration {
	return time.Duration(int64(h.CorrectionNs<<16) >> 16)
}

// Select one sourcePortIdentity and address, separately from its GM. A relay
// can announce a different GM without changing its own packet identity.
// Called under mu, including owner/generation validation and offset updates.
func (s *Server) acceptPacket(p serverMsg) bool {
	if s.owner == nil || !s.allowed[p.Src.IP.String()] || p.ClockIdentity == 0 {
		return false
	}
	var port uint16
	switch x := p.Msg.(type) {
	case *wire.AnnounceMsg:
		port = x.PortNumber
	case *wire.SyncMsg:
		port = x.PortNumber
	case *wire.FollowUpMsg:
		port = x.PortNumber
	case *wire.DelRespMsg:
		port = x.PortNumber
	default:
		return false
	}
	announce, _ := p.Msg.(*wire.AnnounceMsg)
	if s.sourceID == 0 {
		if s.master != 0 && s.master != p.ClockIdentity && (announce == nil || announce.GMIdentity != s.master) {
			return false
		}
		s.sourceID = p.ClockIdentity
		s.sourceIP = p.Src.IP.String()
		s.sourcePort = port
		if s.master == 0 {
			s.master = p.ClockIdentity
		}
		s.generation++
	} else if s.sourceID != p.ClockIdentity || s.sourceIP != p.Src.IP.String() || s.sourcePort != port {
		return false
	}
	if announce != nil && announce.GMIdentity != 0 && announce.GMIdentity != s.grandmaster {
		s.logf("PTP grandmaster source=%016x old=%016x new=%016x", s.sourceID, s.grandmaster, announce.GMIdentity)
		if s.hasTiming {
			s.transitioning = true
			s.haveCandidate = false
			s.grandmasterChangedAt = p.LocalTimeStamp
		}
		s.grandmaster = announce.GMIdentity
		s.generation++
	}
	return true
}

func decodePacket(b []byte) (encoding.BinaryUnmarshaler, error) {
	if len(b) < wire.HeaderLen {
		return nil, fmt.Errorf("truncated PTP header")
	}
	h := new(wire.Header)
	if err := h.UnmarshalBinary(b[:wire.HeaderLen]); err != nil {
		return nil, err
	}
	if h.VersionPTP != wire.Version2 {
		return nil, fmt.Errorf("unsupported PTP version")
	}
	if int(h.MessageLength) > len(b) || int(h.MessageLength) < wire.HeaderLen {
		return nil, fmt.Errorf("invalid PTP length")
	}
	b = b[:h.MessageLength]
	var msg encoding.BinaryUnmarshaler
	switch h.MessageType {
	case wire.AnnounceMsgType:
		msg = &wire.AnnounceMsg{}
	case wire.SyncMsgType:
		msg = &wire.SyncMsg{}
	case wire.FollowUpMsgType:
		if len(b) < wire.HeaderLen+wire.FollowUpPayloadLen {
			return nil, fmt.Errorf("truncated PTP FollowUp")
		}
		// The dependency expects exactly the fixed timestamp payload; Apple adds profile TLVs.
		b = b[:wire.HeaderLen+wire.FollowUpPayloadLen]
		msg = &wire.FollowUpMsg{}
	case wire.DelayRespMsgType:
		msg = &wire.DelRespMsg{}
	default:
		return nil, nil
	}
	if err := safeUnmarshal(msg, b); err != nil {
		return nil, err
	}
	return msg, nil
}
func safeUnmarshal(m encoding.BinaryUnmarshaler, b []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("truncated PTP payload")
		}
	}()
	return m.UnmarshalBinary(b)
}

// Selection changes are scoped to the claimed audio owner, so stale cleanup
// from the previous session cannot reset a new sender's timing state.
func (s *Server) Bind(owner any, peers []net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner = owner
	s.allowed = peerMap(peers)
	s.master = 0
	s.sourceID = 0
	s.sourceIP = ""
	s.sourcePort = 0
	s.grandmaster = 0
	s.hasTiming = false
	s.transitioning = false
	s.haveCandidate = false
	if s.clock != nil {
		s.clock.resetTimelines()
	}
	s.generation++
}
func (s *Server) Clear(owner any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == owner {
		s.owner = nil
		s.allowed = nil
		s.master = 0
		s.sourceID = 0
		s.sourceIP = ""
		s.sourcePort = 0
		s.grandmaster = 0
		s.hasTiming = false
		s.transitioning = false
		s.haveCandidate = false
		if s.clock != nil {
			s.clock.resetTimelines()
		}
		s.generation++
	}
}
func (s *Server) SetPeers(owner any, peers []net.IP, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner {
		return
	}
	allowed := peerMap(peers)
	changed := !maps.Equal(s.allowed, allowed) || (id != 0 && id != s.master)
	s.allowed = allowed
	if id != 0 {
		s.master = id
		if s.clock != nil {
			s.clock.pinTimeline(id)
		}
	}
	if s.sourceID != 0 && (!allowed[s.sourceIP] || (id != 0 && id != s.sourceID && id != s.grandmaster)) {
		s.sourceID = 0
		s.sourceIP = ""
		s.sourcePort = 0
		s.grandmaster = 0
	}
	if changed {
		s.haveCandidate = false
		s.generation++
	}
}
func peerMap(peers []net.IP) map[string]bool {
	m := make(map[string]bool)
	for _, ip := range peers {
		if ip != nil {
			m[ip.String()] = true
		}
	}
	return m
}
func (s *Server) accept(ip net.IP, id uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil || !s.allowed[ip.String()] || id == 0 {
		return s.generation, false
	}
	if s.master == 0 {
		s.master = id
		s.generation++
	}
	return s.generation, s.master == id
}

func (s *Server) applyOffset(generation uint64, offset time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil || s.generation != generation {
		return false
	}
	id := s.grandmaster
	if id == 0 {
		id = s.master
	}
	s.clock.OffsetForTimeline(id, offset)
	return true
}

func (s *Server) logf(format string, args ...any) {
	logger := s.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf(format, args...)
}
