// Package event implements the authenticated AirPlay 2 reverse event channel.
package event

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"howett.net/plist"
)

var (
	ErrUnavailable = errors.New("AirPlay sender control unavailable")
	ErrUnsupported = errors.New("unsupported AirPlay sender command")
)

const maxSessions = 8

// Hub associates incoming event sockets with authenticated RTSP peers. Register
// must precede advertising the event port to the sender. An event EOF, command
// failure, or the owning RTSP connection closes the Session and registration.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
	readers  sync.WaitGroup
}

func NewHub() *Hub { return &Hub{sessions: make(map[string]*Session)} }

// Session owns one authenticated peer's event channel. Commands wait for a real
// sender acknowledgment; a successful call does not assert playback state.
type Session struct {
	hub     *Hub
	peer    string
	mu      sync.Mutex
	conn    *encryptedConn
	pending chan error
	cipher  *eventCipher
	groupID string
	closed  bool
	done    chan struct{}
	request chan struct{}
}

func (h *Hub) Register(peerIP net.IP, sharedSecret []byte) (*Session, error) {
	if peerIP.To16() == nil || peerIP.IsUnspecified() {
		return nil, fmt.Errorf("invalid event peer IP")
	}
	crypt, err := newEventCipher(sharedSecret)
	if err != nil {
		return nil, err
	}
	s := &Session{hub: h, peer: peerIP.String(), cipher: crypt, done: make(chan struct{}), request: make(chan struct{}, 1)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrUnavailable
	}
	if h.sessions[s.peer] != nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: peer already owns an event session", ErrUnavailable)
	}
	if len(h.sessions) >= maxSessions {
		h.mu.Unlock()
		return nil, fmt.Errorf("too many event sessions")
	}
	h.sessions[s.peer] = s
	h.mu.Unlock()
	return s, nil
}
func (s *Session) SetGroupID(groupID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.groupID = groupID
	}
}
func (s *Session) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.conn != nil
}
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	c := s.conn
	s.conn = nil
	s.pending = nil
	s.cipher = nil
	s.groupID = ""
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
	s.hub.mu.Lock()
	if s.hub.sessions[s.peer] == s {
		delete(s.hub.sessions, s.peer)
	}
	s.hub.mu.Unlock()
}
func (h *Hub) attach(c net.Conn) {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		c.Close()
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		c.Close()
		return
	}
	h.mu.Lock()
	s := h.sessions[ip.String()]
	if s == nil || h.closed {
		h.mu.Unlock()
		c.Close()
		return
	}
	s.mu.Lock()
	if s.closed || s.conn != nil {
		s.mu.Unlock()
		h.mu.Unlock()
		c.Close()
		return
	}
	conn := &encryptedConn{Conn: c, crypt: s.cipher}
	s.conn = conn
	s.cipher = nil
	s.mu.Unlock()
	h.readers.Go(func() { s.readResponses(conn) })
	h.mu.Unlock()
}

// One reader owns decryption and acknowledgment parsing for the entire socket
// lifetime, including idle periods. An idle EOF invalidates Available without
// requiring another command to discover it. Replies without an outstanding
// command, or extra buffered replies, cannot satisfy a future command.
func (s *Session) readResponses(c *encryptedConn) {
	defer s.Close()
	r := bufio.NewReaderSize(c, 4096)
	for {
		if _, err := r.Peek(1); err != nil {
			return
		}
		s.mu.Lock()
		pending, closed := s.pending, s.closed
		s.mu.Unlock()
		if closed || pending == nil {
			return
		}
		err := readAcknowledgment(r)
		if err == nil && r.Buffered() != 0 {
			err = fmt.Errorf("unexpected data after event acknowledgment")
		}
		if err == nil {
			// The next idle read must not inherit this command's deadline.
			err = c.SetReadDeadline(time.Time{})
		}
		s.mu.Lock()
		if s.pending == pending {
			s.pending = nil
			pending <- err
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Serve accepts only registered peers. Idle sockets have no read deadline:
// AirPlay senders can legitimately remain silent until a remote command.
func (h *Hub) Serve(ctx context.Context, l net.Listener) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	defer func() {
		l.Close()
		h.mu.Lock()
		h.closed = true
		sessions := make([]*Session, 0, len(h.sessions))
		for _, s := range h.sessions {
			sessions = append(sessions, s)
		}
		h.mu.Unlock()
		for _, s := range sessions {
			s.Close()
		}
		h.readers.Wait()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		h.attach(c)
	}
}

// Serve preserves the standalone listener API. Remote control requires a shared
// Hub registered by the authenticated RTSP handler.
func Serve(ctx context.Context, l net.Listener) error { return NewHub().Serve(ctx, l) }

// Wire command numbers and archive schema are documented by Shairport Sync:
// https://github.com/mikebrady/shairport-sync/blob/master/remote/remote.h
// https://github.com/mikebrady/shairport-sync/blob/master/remote/remote.c
func commandBody(action, group string) ([]byte, error) {
	var opcode string
	switch action {
	case "play", "resume":
		opcode = "0"
	case "pause":
		opcode = "1"
	case "stop":
		opcode = "3"
	case "next":
		opcode = "4"
	case "previous":
		opcode = "5"
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, action)
	}
	if group == "" || len(group) > 256 {
		return nil, fmt.Errorf("%w: sender group ID missing", ErrUnavailable)
	}
	archive, err := plist.Marshal(map[string]any{
		"$version": uint64(100000), "$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
		"$objects": []any{"$null", map[string]any{"NS.objects": []any{plist.UID(2)}, "$class": plist.UID(3)}, group, map[string]any{"$classname": "NSMutableArray", "$classes": []string{"NSMutableArray", "NSArray", "NSObject"}}},
	}, plist.BinaryFormat)
	if err != nil {
		return nil, err
	}
	return plist.Marshal(map[string]any{"type": "sendMediaRemoteCommand", "modernMediaRemoteCommand": opcode, "params": map[string]any{
		"kMRMediaRemoteOptionSendOptionsNumber": uint64(0), "kMRMediaRemoteOptionCommandID": uuid.NewString(), "kMRMediaRemoteOptionIsRedirectingCommand": true, "kMRMediaRemoteOptionDestinationDeviceUIDs": archive,
	}}, plist.BinaryFormat)
}
func (s *Session) Send(ctx context.Context, action string) error {
	s.mu.Lock()
	group := s.groupID
	s.mu.Unlock()
	body, err := commandBody(action, group)
	if err != nil {
		return err
	}
	return s.post(ctx, body)
}

// SetVolume notifies the sender of the receiver's unit volume (0..1), keeping
// the sender's volume slider synchronized after local software gain changes.
func (s *Session) SetVolume(ctx context.Context, unit float64) error {
	if math.IsNaN(unit) || math.IsInf(unit, 0) || unit < 0 || unit > 1 {
		return fmt.Errorf("invalid AirPlay unit volume")
	}
	body, err := plist.Marshal(map[string]any{"type": "sendMediaRemoteCommand", "value": "dvlc", "volume": unit, "params": map[string]any{"volume": unit}}, plist.BinaryFormat)
	if err != nil {
		return err
	}
	return s.post(ctx, body)
}
func (s *Session) post(ctx context.Context, body []byte) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.request <- struct{}{}:
		defer func() { <-s.request }()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	c := s.conn
	closed := s.closed
	ack := make(chan error, 1)
	if !closed && c != nil {
		s.pending = ack
	}
	s.mu.Unlock()
	if closed || c == nil {
		return ErrUnavailable
	}
	// Cancellation invalidates this channel, since an outstanding acknowledgment
	// cannot safely be attributed to a subsequent request.
	stop := context.AfterFunc(ctx, s.Close)
	defer stop()
	defer func() {
		if err != nil {
			s.Close()
			if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = c.SetDeadline(deadline); err != nil {
		return err
	}
	defer c.SetWriteDeadline(time.Time{})
	request := fmt.Appendf(nil, "POST /command RTSP/1.0\r\nContent-Length: %d\r\nContent-Type: application/x-apple-binary-plist\r\n\r\n", len(body))
	request = append(request, body...)
	if _, err = c.Write(request); err != nil {
		return err
	}
	select {
	case err = <-ack:
	case <-s.done:
		// A sender may close immediately after its complete acknowledgment.
		// Preserve that result even if both it and EOF are ready together.
		select {
		case err = <-ack:
		default:
			err = ErrUnavailable
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	return err
}
func readAcknowledgment(r *bufio.Reader) error {
	total := 0
	line := func() (string, error) {
		var out []byte
		for {
			part, prefix, err := r.ReadLine()
			if err != nil {
				return "", err
			}
			total += len(part) + 2
			if total > 16*1024 {
				return "", fmt.Errorf("event response headers too large")
			}
			out = append(out, part...)
			if !prefix {
				return string(out), nil
			}
		}
	}
	status, err := line()
	if err != nil {
		return err
	}
	parts := strings.Fields(status)
	if len(parts) < 2 || (parts[0] != "RTSP/1.0" && parts[0] != "RTSP/1.1") || len(parts[1]) != 3 {
		return fmt.Errorf("invalid event RTSP response")
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("invalid event RTSP status")
	}
	length := 0
	seenLength := false
	for {
		header, err := line()
		if err != nil {
			return err
		}
		if header == "" {
			break
		}
		name, value, ok := strings.Cut(header, ":")
		if !ok {
			return fmt.Errorf("invalid event RTSP header")
		}
		if strings.EqualFold(name, "Content-Length") {
			if seenLength {
				return fmt.Errorf("duplicate event response length")
			}
			seenLength = true
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > 64*1024 {
				return fmt.Errorf("invalid event response length")
			}
		}
	}
	if _, err := io.CopyN(io.Discard, r, int64(length)); err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("AirPlay sender rejected command: RTSP %d", code)
	}
	return nil
}
