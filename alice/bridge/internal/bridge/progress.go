// Modified for Alice Gemini Bridge: native acknowledgement, persona and microphone control fixes.
package bridge

import (
	"encoding/binary"
	"log/slog"
	"time"

	"github.com/coder/websocket"
	directive "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/directives"
	response "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_response"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	stream "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/stream_control"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	deferred "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/streaming"
	"google.golang.org/protobuf/encoding/protowire"
	"quasarbridge/internal/wire"
)

// progress starts only after the executor is known. Stop joins the pump so no
// partial can race with the final answer or continue after cancellation.
func (n *Native) progress(p *nativeRequest) func() { return n.progressAfter(p, 0) }
func (n *Native) progressAfter(p *nativeRequest, minimum time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	ready := make(chan struct{})
	n.wg.Go(func() {
		defer close(done)
		delay := n.ProgressAfter
		if n.VoiceCues != nil {
			delay = minimum
		}
		if delay < minimum {
			delay = minimum
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-stop:
			close(ready)
			return
		case <-p.ctx.Done():
			close(ready)
			return
		case <-timer.C:
		}
		err := n.startProgress(p)
		close(ready)
		if err != nil {
			p.cancel()
			return
		}
		slog.Info("native_progress_started", "request", requestTag(p.id))
		interval := n.ProgressInterval
		if interval <= 0 {
			interval = 4 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				if err := n.deferredPart(p, "", false); err != nil {
					p.cancel()
					return
				}
			}
		}
	})
	if n.VoiceCues != nil && minimum == 0 {
		<-ready
	}
	return func() { close(stop); <-done }
}
func (p *nativeRequest) applicationID() string {
	if id := p.request.GetHeader().GetRequestId(); id != "" {
		return id
	}
	return p.id
}
func (n *Native) beginDeferred(p *nativeRequest) error {
	r := &response.TAliceResponseProto{Header: &response.TAliceResponseProto_THeader{RequestId: new(p.applicationID()), ResponseId: new(p.responseID), DialogId: new(p.request.GetHeader().GetDialogId()), SequenceNumber: new(p.request.GetHeader().GetSequenceNumber())}, VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{HasVoiceResponse: true, OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new("")}}, Response: &response.TAliceResponseProto_TResponse{IsStreaming: true, Directives: []*directive.TDirective{{Type: "client_action", Name: "tts_play_placeholder"}}}}
	// Station schedules post-speech listening from the initial stream envelope.
	// Home replies also require listening for clarification; music may start silent media.
	r.VoiceResponse.ShouldListen = n.ShouldListen && p.listenOnStreamStart
	slog.Info("native_listening", "request", requestTag(p.id), "stage", "initial", "enabled", r.VoiceResponse.ShouldListen)
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: r}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	if err := n.deferredPart(p, "", false); err != nil {
		return err
	}
	// Obtain the provider's actual Opus headers; its speech payload is discarded.
	if n.VoiceCues != nil {
		return n.synthesize(p, ThinkingPhrase)
	}
	return n.synthesize(p, "а")
}
func (n *Native) deferredPart(p *nativeRequest, text string, last bool) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.partial++
	base := &deferred.TDeferedAliceResponseProto_TBaseResponse{}
	voice := text
	if last {
		silentPlayback := n.hasSilentPlayback(p)
		base.ShouldListen = n.ShouldListen && !silentPlayback
		slog.Info("native_listening", "request", requestTag(p.id), "stage", "final", "enabled", base.ShouldListen, "answer_bytes", len(text))
		if silentPlayback {
			voice = ""
		}
		base.Directives = n.takeSound(p)
	}
	if text != "" {
		base.Text = new(text)
	}
	// Older Station SDKs still consume reserved field 2 (Voice), separately
	// from Text. Observed cloud responses send speech deltas here.
	unknown := protowire.AppendTag(nil, 2, protowire.BytesType)
	unknown = protowire.AppendString(unknown, voice)
	base.ProtoReflect().SetUnknown(unknown)
	r := &deferred.TDeferedAliceResponseProto{RequestId: p.applicationID(), ResponsePartialNum: p.partial, IsLast: last, TDeferredResponse: &deferred.TDeferedAliceResponseProto_BaseResponse{BaseResponse: base}}
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_DeferredAliceResponse{DeferredAliceResponse: r}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	n.captureDelivered(p, text, last, len(base.Directives) > 0, true)
	return nil
}
func (n *Native) finishDeferred(p *nativeRequest, text string) error {
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case <-p.progressAudioDone:
	}
	if n.hasSilentPlayback(p) {
		if err := n.finishSilentAudio(p); err != nil {
			return err
		}
		return n.deferredPart(p, text, true)
	}
	n.mu.Lock()
	p.ttsID = newID()
	n.tts[p.ttsID] = p
	n.mu.Unlock()
	if err := n.deferredPart(p, text, true); err != nil {
		return err
	}
	return n.synthesize(p, text)
}

// Content audio must start after the silent Dialog player releases its focus.
func (n *Native) finishSilentAudio(p *nativeRequest) error {
	n.mu.Lock()
	p.audioWaiting = false
	end := p.audio.finishSilence()
	streamID := p.audioStream
	n.mu.Unlock()
	if err := n.down(p.ctx, websocket.MessageBinary, append(binary.BigEndian.AppendUint32(nil, streamID), end...)); err != nil {
		return err
	}
	m := &server.TServerMessage{Message: &server.TServerMessage_StreamControl{StreamControl: &stream.TStreamControl{MessageId: p.id, StreamId: int64(streamID), Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{}}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.down(p.ctx, websocket.MessageBinary, b)
}

// Called under n.mu after the first synthesized segment closes. Keep the audio
// alive through final TTS synthesis, until its TtsSpeak starts the next segment.
func (n *Native) waitAudio(p *nativeRequest) {
	n.wg.Go(func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				n.mu.Lock()
				if p.canceled || !p.audioWaiting {
					n.mu.Unlock()
					return
				}
				b := append(binary.BigEndian.AppendUint32(nil, p.audioStream), p.audio.silence()...)
				err := n.down(p.ctx, websocket.MessageBinary, b)
				n.mu.Unlock()
				if err != nil {
					p.cancel()
					return
				}
			}
		}
	})
}
