// Modified for Alice Gemini Bridge: native acknowledgement, persona and microphone control fixes.
package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	stream "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/stream_control"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"quasarbridge/internal/llm"
	"quasarbridge/internal/wire"
)

func cuesForTest(t *testing.T) *VoiceCues {
	t.Helper()
	c, err := OpenVoiceCues(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.save(ThinkingPhrase, speechFixture(123)); err != nil {
		t.Fatal(err)
	}
	return c
}
func micInput(id string, streamID int64) []byte {
	m := wire.Decode(soundInput(""), wire.ClientToServer).Client
	e := m.GetEvent()
	e.Header.MessageId = id
	e.Header.StreamId = new(streamID)
	e.GetTextInput().GetRequest().GetEvent().Text = new("")
	e.Event = &client.TClientMessage_TEvent_VoiceInput{VoiceInput: e.GetTextInput()}
	return aa(m)
}
func micClose(id string, streamID int64) []byte {
	return aa(&client.TClientMessage{Message: &client.TClientMessage_StreamControl{StreamControl: &stream.TStreamControl{MessageId: id, StreamId: streamID, Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{}}}}})
}
func TestRemovedEasterEggCacheIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cutoff.ogg"), speechFixture(77), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenVoiceCues(dir)
	if err != nil {
		t.Fatal(err)
	}
	removed := "Долго еще пиздеть будешь?"
	if len(c.load(removed)) != 0 {
		t.Fatal("removed easter egg loaded from disk")
	}
	if c.save(removed, speechFixture(88)) == nil {
		t.Fatal("removed easter egg is still supported")
	}
}
func TestVoiceCuesPersistOnlyCompleteOpus(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenVoiceCues(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.save(ThinkingPhrase, []byte("not audio")) == nil {
		t.Fatal("invalid audio cached")
	}
	if err = c.save(ThinkingPhrase, speechFixture(77)); err != nil {
		t.Fatal(err)
	}
	c, err = OpenVoiceCues(dir)
	if err != nil || len(c.load(ThinkingPhrase)) == 0 {
		t.Fatal("cache did not survive reopen", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("unexpected cache entries", len(entries))
	}
	st, _ := os.Stat(filepath.Join(dir, entries[0].Name()))
	if st.Mode().Perm() != 0600 {
		t.Fatal("unsafe cache mode")
	}
}
func TestCachedThinkingPrecedesAnswerWithoutSynthesis(t *testing.T) {
	release := make(chan struct{})
	n, up, down := soundSession(t, delayedRoute{release})
	n.VoiceCues = cuesForTest(t)
	if err := n.Client(t.Context(), websocket.MessageBinary, soundInput("question")); err != nil {
		t.Fatal(err)
	}
	var streamID int64
	for streamID == 0 {
		f := nextSoundFrame(t, down)
		if f.Kind() == "TtsSpeak" {
			streamID = f.Server.GetEvent().GetHeader().GetStreamId()
		}
	}
	for len(up) > 0 {
		if f := <-up; f.Kind() == "TtsGenerate" {
			t.Fatal("cached cue was synthesized again")
		}
	}
	close(release)
	for {
		f := nextSoundFrame(t, up)
		if f.Kind() == "TtsGenerate" {
			if f.Client.GetEvent().GetTtsGenerate().GetText() != "Готовый ответ" {
				t.Fatal("unexpected synthesis")
			}
			feedSpeech(t, n, f.Client.GetEvent().GetHeader().GetMessageId(), 88, false)
			break
		}
	}
	for {
		f := nextSoundFrame(t, down)
		if f.Kind() == "TtsSpeak" {
			t.Fatal("second player")
		}
		if sc := f.Server.GetStreamControl(); sc != nil && sc.GetClose() != nil {
			if sc.GetStreamId() != streamID {
				t.Fatal("second audio stream")
			}
			break
		}
	}
}
func TestMicrophoneRemainsOpenPastTenSecondsWithoutLocalReply(t *testing.T) {
	n, up, down := soundSession(t, nil)
	n.VoiceCues = cuesForTest(t)
	if err := n.Client(t.Context(), websocket.MessageBinary, micInput("long-mic", 23)); err != nil {
		t.Fatal(err)
	}
	if f := nextSoundFrame(t, up); f.Kind() != "VoiceInput" {
		t.Fatal("missing mic input")
	}
	time.Sleep(10100 * time.Millisecond)
	if len(down) != 0 {
		t.Fatal("local reply or recording interruption without final ASR")
	}
	audio := append(binary.BigEndian.AppendUint32(nil, 23), []byte("still-recording")...)
	if err := n.Client(t.Context(), websocket.MessageBinary, audio); err != nil {
		t.Fatal(err)
	}
	if f := nextSoundFrame(t, up); string(f.Raw) != string(audio) {
		t.Fatal("ongoing audio no longer forwarded")
	}
	if err := n.Client(t.Context(), websocket.MessageBinary, micClose("long-mic", 23)); err != nil {
		t.Fatal(err)
	}
	if f := nextSoundFrame(t, up); f.Kind() != "StreamControl" {
		t.Fatal("mic close not forwarded")
	}
}
func TestMicrophoneControlOwnMessageIDIsTransparent(t *testing.T) {
	for _, fromServer := range []bool{false, true} {
		for _, spotter := range []bool{false, true} {
			t.Run(fmt.Sprintf("server=%v/spotter=%v", fromServer, spotter), func(t *testing.T) {
				n, up, down := soundSession(t, nil)
				n.VoiceCues = cuesForTest(t)
				if err := n.Client(t.Context(), websocket.MessageBinary, micInput("voice-request", 23)); err != nil {
					t.Fatal(err)
				}
				_ = nextSoundFrame(t, up)
				sc := &stream.TStreamControl{MessageId: "independent-control-id", StreamId: 23, Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{}}}
				if spotter {
					sc.Action = &stream.TStreamControl_SpotterEnd{SpotterEnd: &stream.TStreamControl_TActionSpotterEnd{}}
				}
				var raw []byte
				var out chan wire.Frame
				if fromServer {
					raw = aa(&server.TServerMessage{Message: &server.TServerMessage_StreamControl{StreamControl: sc}})
					out = down
					if err := n.Server(t.Context(), websocket.MessageBinary, raw); err != nil {
						t.Fatal(err)
					}
				} else {
					raw = aa(&client.TClientMessage{Message: &client.TClientMessage_StreamControl{StreamControl: sc}})
					out = up
					if err := n.Client(t.Context(), websocket.MessageBinary, raw); err != nil {
						t.Fatal(err)
					}
				}
				if len(out) != 1 {
					t.Fatal("valid control with own message ID was dropped", len(out))
				}
				if f := <-out; !bytes.Equal(f.Raw, raw) {
					t.Fatal("microphone control bytes changed")
				}
			})
		}
	}
}
func TestStaleCloseCannotAffectReusedMic(t *testing.T) {
	for _, fromServer := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "server"}[fromServer], func(t *testing.T) {
			n, up, down := soundSession(t, nil)
			n.VoiceCues = cuesForTest(t)
			_ = n.Client(t.Context(), websocket.MessageBinary, micInput("old", 23))
			_ = n.Client(t.Context(), websocket.MessageBinary, micInput("new", 23))
			for len(up) > 0 {
				<-up
			}
			if fromServer {
				_ = n.Server(t.Context(), websocket.MessageBinary, aa(&server.TServerMessage{Message: &server.TServerMessage_StreamControl{StreamControl: &stream.TStreamControl{MessageId: "old", StreamId: 23, Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{}}}}}))
			} else {
				_ = n.Client(t.Context(), websocket.MessageBinary, micClose("old", 23))
			}
			if len(up) > 0 || len(down) > 0 {
				t.Fatal("stale close forwarded")
			}
			audio := append(binary.BigEndian.AppendUint32(nil, 23), []byte("current-mic")...)
			_ = n.Client(t.Context(), websocket.MessageBinary, audio)
			if f := nextSoundFrame(t, up); string(f.Raw) != string(audio) {
				t.Fatal("new mic audio blocked")
			}
		})
	}
}
func TestCanceledTTSBlocksLateAudio(t *testing.T) {
	n, up, down := soundSession(t, resolverFunc(func(context.Context, string) (Resolution, error) { return Resolution{Route: "llm", Answer: "old"}, nil }))
	n.VoiceCues = cuesForTest(t)
	_ = n.Client(t.Context(), websocket.MessageBinary, soundInput("old"))
	id := ""
	for id == "" {
		f := nextSoundFrame(t, up)
		if f.Kind() == "TtsGenerate" {
			id = f.Client.GetEvent().GetHeader().GetMessageId()
		}
	}
	for len(down) > 0 {
		<-down
	}
	_ = n.Client(t.Context(), websocket.MessageBinary, micInput("new", 23))
	feedSpeech(t, n, id, 77, false)
	if len(down) > 0 {
		t.Fatal("canceled TTS leaked downstream audio")
	}
}
func TestConfiguredPersonaPreservesBaseSafety(t *testing.T) {
	e := &Engine{SystemPrompt: "Едкий русский сленг и мат.", LLM: completionFunc(func(_ context.Context, m []llm.Message, _ bool) (string, error) {
		if !strings.Contains(m[0].Content, "Едкий русский сленг и мат.") || !strings.Contains(m[0].Content, "Не заявляй о выполнении") {
			t.Fatal("persona replaced safety or is absent")
		}
		return "Готово.", nil
	})}
	if _, err := e.Fallback(t.Context(), "тест"); err != nil {
		t.Fatal(err)
	}
}
