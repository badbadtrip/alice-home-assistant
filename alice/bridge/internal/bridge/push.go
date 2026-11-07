package bridge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	directives "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/directives"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	"github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/push"
	"github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"google.golang.org/protobuf/types/known/structpb"

	"quasarbridge/internal/wire"
)

// MaxVolumeLevel is the top of the Station's own 0-10 volume scale.
const MaxVolumeLevel = 10

// ErrNoStation means the Station has no live uniproxy connection to push to.
var ErrNoStation = errors.New("station is not connected")

// stationLink remembers the newest Station connection. A reconnecting Station
// can open the new socket before the old one closes, so only the owner clears it.
type stationLink struct {
	mu   sync.Mutex
	send *Sender
}

func (l *stationLink) attach(send Sender) (detach func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current := &send
	l.send = current
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.send == current {
			l.send = nil
		}
	}
}

func (l *stationLink) write(ctx context.Context, frame []byte) error {
	l.mu.Lock()
	send := l.send
	l.mu.Unlock()
	if send == nil {
		return ErrNoStation
	}
	return (*send)(ctx, websocket.MessageBinary, frame)
}

// pushFrame wraps directives in a server push, which needs no pending request.
func pushFrame(items ...*directives.TDirective) ([]byte, error) {
	ack := time.Now().UnixMilli()
	dedup := newID()
	event := &server.TServerMessage_TEvent{
		Header: &header.TEventHeader{MessageId: newID(), Ack: &ack},
		Event:  &server.TServerMessage_TEvent_Push{Push: &push.TPush{PushIds: []string{newID()}, DeduplicationPushId: &dedup, Directives: items}},
	}
	return (wire.Frame{Server: &server.TServerMessage{Message: &server.TServerMessage_Event{Event: event}}, Prefix: []byte("AAPI")}).Marshal()
}

func volumeDirective(level int) (*directives.TDirective, error) {
	if level < 0 || level > MaxVolumeLevel {
		return nil, errors.New("volume level out of range")
	}
	payload, err := structpb.NewStruct(map[string]any{"new_level": level})
	if err != nil {
		return nil, err
	}
	return &directives.TDirective{Type: "client_action", Name: "sound_set_level", AnalyticsType: "sound_set_level", IsLedSilent: true, PayloadType: &directives.TDirective_Payload{Payload: payload}}, nil
}

// Silence stops the Station's own audio: Alice's reply and stock music alike.
func (p *Proxy) Silence(ctx context.Context) error {
	empty, _ := structpb.NewStruct(map[string]any{})
	payload := &directives.TDirective_Payload{Payload: empty}
	frame, err := pushFrame(
		&directives.TDirective{Type: "client_action", Name: "audio_stop", AnalyticsType: "audio_stop", IsLedSilent: true, PayloadType: payload},
		&directives.TDirective{Type: "client_action", Name: "player_pause", AnalyticsType: "player_pause", IsLedSilent: true, PayloadType: payload},
	)
	if err != nil {
		return err
	}
	return p.station.write(ctx, frame)
}

// SetVolume sets the Station volume on its 0-10 scale.
func (p *Proxy) SetVolume(ctx context.Context, level int) error {
	directive, err := volumeDirective(level)
	if err != nil {
		return err
	}
	frame, err := pushFrame(directive)
	if err != nil {
		return err
	}
	return p.station.write(ctx, frame)
}

// VolumeHandler serves GET /volume?level=N. It is GET because BusyBox wget on
// Lite 2 has no --post-data; the bridge listens on loopback only.
func (p *Proxy) VolumeHandler(w http.ResponseWriter, r *http.Request) {
	level, err := strconv.Atoi(r.URL.Query().Get("level"))
	if err != nil || level < 0 || level > MaxVolumeLevel {
		http.Error(w, "level must be 0-10", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	switch err := p.SetVolume(ctx, level); {
	case errors.Is(err, ErrNoStation):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case err != nil:
		http.Error(w, "push failed", http.StatusBadGateway)
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","level":` + strconv.Itoa(level) + "}"))
	}
}
