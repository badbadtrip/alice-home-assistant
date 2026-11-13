package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	common "github.com/freequasar/quasar-go-genproto/proto/alice/megamind/protos/common"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	asr "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result"
	recognition "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result/common"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	validation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/spotter_validation"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
)

func TestSpeakerThatLostMultiactivationDoesNotExecute(t *testing.T) {
	for _, lost := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		ran := make(chan string, 1)
		n := NewNative(ctx, func(context.Context, websocket.MessageType, []byte) error { return nil },
			func(context.Context, websocket.MessageType, []byte) error { return nil },
			resolverFunc(func(_ context.Context, text string) (Resolution, error) {
				ran <- text
				return Resolution{Route: "home", Answer: "Готово"}, nil
			}))
		voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "voice", StreamId: new(int64(1))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
		_ = n.Client(ctx, websocket.MessageBinary, voice)
		check := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "voice"}, Event: &server.TServerMessage_TEvent_SpotterValidation{SpotterValidation: &validation.TSpotterValidation{Result: validation.TSpotterValidation_Ok, CanceledCauseOfMultiactivation: new(lost)}}}}})
		_ = n.Server(ctx, websocket.MessageBinary, check)
		final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "voice"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("включи музыку")}}}}}}})
		_ = n.Server(ctx, websocket.MessageBinary, final)
		select {
		case <-ran:
			if lost {
				t.Fatal("the speaker that lost the activation ran the command")
			}
		case <-time.After(200 * time.Millisecond):
			if !lost {
				t.Fatal("the chosen speaker must run the command")
			}
		}
		n.Close()
		cancel()
	}
}
