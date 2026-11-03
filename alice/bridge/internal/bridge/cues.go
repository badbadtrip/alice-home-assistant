// Modified for Alice Gemini Bridge: native acknowledgement, persona and microphone control fixes.
package bridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	stream "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/stream_control"
	tts "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/tts_speak"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"quasarbridge/internal/wire"
)

const ThinkingPhrase = "Думаю"
const cueLimit = 512 << 10

var cueStreams atomic.Uint32

// Only the acknowledgement is persisted, never human speech.
type VoiceCues struct {
	mu    sync.Mutex
	dir   string
	audio map[string][]byte
}

func cueFile(text string) string {
	switch text {
	case ThinkingPhrase:
		return "thinking.ogg"

	}
	return ""
}
func OpenVoiceCues(dir string) (*VoiceCues, error) {
	if dir == "" {
		return nil, errors.New("voice cue cache directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	c := &VoiceCues{dir: dir, audio: map[string][]byte{}}
	for _, text := range []string{ThinkingPhrase} {
		f, err := os.Open(filepath.Join(dir, cueFile(text)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(f, cueLimit+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		if err = validateCue(b); err != nil {
			return nil, errors.New("invalid saved native cue")
		}
		c.audio[text] = b
	}
	return c, nil
}
func validateCue(b []byte) error {
	if len(b) == 0 || len(b) > cueLimit {
		return errors.New("invalid native cue size")
	}
	var o oggSpeech
	if _, err := o.feed(b); err != nil {
		return err
	}
	return o.endSource()
}
func (c *VoiceCues) load(text string) []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.audio[text]
}
func (c *VoiceCues) save(text string, b []byte) error {
	name := cueFile(text)
	if name == "" {
		return errors.New("not a static cue")
	}
	if err := validateCue(b); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.audio[text]) > 0 {
		return nil
	}
	f, err := os.CreateTemp(c.dir, ".cue-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(c.dir, name)); err != nil {
		return err
	}
	c.audio[text] = append([]byte(nil), b...)
	slog.Info("native_cue_saved", "cue", name, "bytes", len(b))
	return nil
}
func (n *Native) replayCue(p *nativeRequest, b []byte) error {
	// High positive IDs avoid the small cloud/input stream range; still check live IDs.
	n.mu.Lock()
	var id uint32
	for {
		id = 0x60000000 | (cueStreams.Add(1) & 0x0fffffff)
		if n.streams[id] == nil && n.blocked[id] == nil && id != uint32(p.header.GetStreamId()) {
			break
		}
	}
	ttsID := p.ttsID
	n.mu.Unlock()
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: ttsID, StreamId: new(int64(id))}, Event: &server.TServerMessage_TEvent_TtsSpeak{TtsSpeak: &tts.TTtsSpeak{Format: "audio/opus"}}}}}
	raw, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err = n.Server(p.ctx, websocket.MessageBinary, raw); err != nil {
		return err
	}
	for len(b) > 0 {
		count := min(len(b), 16384)
		if err = n.Server(p.ctx, websocket.MessageBinary, append(binary.BigEndian.AppendUint32(nil, id), b[:count]...)); err != nil {
			return err
		}
		b = b[count:]
	}
	m = &server.TServerMessage{Message: &server.TServerMessage_StreamControl{StreamControl: &stream.TStreamControl{MessageId: ttsID, StreamId: int64(id), Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{}}}}}
	raw, err = (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	slog.Info("native_cue_replayed", "request", requestTag(p.id))
	return n.Server(p.ctx, websocket.MessageBinary, raw)
}

func (n *Native) micFrame(f wire.Frame, data []byte) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if sc := f.Client.GetStreamControl(); sc != nil {
		if p := n.microphones[uint32(sc.GetStreamId())]; p != nil {
			blocked := n.staleMicControlLocked(sc) || p.canceled
			action := "other"
			if sc.GetSpotterEnd() != nil {
				action = "spotter_end"
			} else if sc.GetClose() != nil {
				action = "close"
			}
			slog.Info("native_mic_control", "request", requestTag(p.id), "action", action, "id_matches_request", sc.GetMessageId() == p.id, "id_is_known_request", n.requests[sc.GetMessageId()] != nil, "forwarded", !blocked)
			return blocked
		}
	} else if f.Client == nil && len(data) >= 4 {
		if p := n.microphones[binary.BigEndian.Uint32(data)]; p != nil {
			return p.canceled
		}
	}
	return false
}

// StreamControl can carry its own message ID. Suppress only a known stale
// request on a reused microphone stream, not an unfamiliar control ID.
func (n *Native) staleMicControlLocked(sc *stream.TStreamControl) bool {
	owner := n.microphones[uint32(sc.GetStreamId())]
	old := n.requests[sc.GetMessageId()]
	return owner != nil && old != nil && old != owner && old.canceled &&
		!old.isText && old.header.StreamId != nil && old.header.GetStreamId() == sc.GetStreamId()
}

// Used before a new owner starts; correlation entries stay to block late responses.
func (n *Native) supersedeLocked(p *nativeRequest) {
	if n.VoiceCues == nil {
		return
	}
	for _, old := range n.requests {
		if old != p && old.speaker == p.speaker && !old.canceled {
			old.canceled = true
			old.cancel()
		}
	}
}
func (n *Native) registerMicrophoneLocked(p *nativeRequest) {
	if n.VoiceCues != nil && !p.isText && p.header.StreamId != nil {
		n.microphones[uint32(p.header.GetStreamId())] = p
	}
}

func (n *Native) startProgress(p *nativeRequest) error {
	n.mu.Lock()
	if p.canceled || p.ctx.Err() != nil {
		n.mu.Unlock()
		return context.Canceled
	}
	p.local = true
	p.streaming = true
	p.responseID = newID()
	p.ttsID = newID()
	p.progressTTS = p.ttsID
	p.progressAudioDone = make(chan struct{})
	n.tts[p.ttsID] = p
	n.mu.Unlock()
	if p.gated {
		if err := n.sendCancel(p.ctx, p.id); err != nil {
			return err
		}
	} else if err := n.ack(p); err != nil {
		return err
	}
	return n.beginDeferred(p)
}
