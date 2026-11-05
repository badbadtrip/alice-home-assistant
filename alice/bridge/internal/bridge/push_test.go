package bridge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"google.golang.org/protobuf/proto"
)

func assertVolumePush(t *testing.T, b []byte, level float64) {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("AAPI")) {
		t.Fatalf("missing AAPI prefix: %q", b)
	}
	var m server.TServerMessage
	if err := proto.Unmarshal(b[4:], &m); err != nil {
		t.Fatal(err)
	}
	push := m.GetEvent().GetPush()
	if push == nil || len(push.Directives) != 1 || len(push.PushIds) != 1 {
		t.Fatalf("not a single-directive push: %v", &m)
	}
	d := push.Directives[0]
	if d.Name != "sound_set_level" || d.GetPayload().Fields["new_level"].GetNumberValue() != level {
		t.Fatalf("wrong directive: %v", d)
	}
}

func TestVolumeRejectsLevelsOffTheStationScale(t *testing.T) {
	for _, level := range []int{-1, 11} {
		if err := new(Proxy).SetVolume(context.Background(), level); err == nil || errors.Is(err, ErrNoStation) {
			t.Fatalf("level %d: %v", level, err)
		}
	}
}

func TestStationLinkKeepsNewestConnection(t *testing.T) {
	var link stationLink
	got := ""
	sender := func(name string) Sender {
		return func(context.Context, websocket.MessageType, []byte) error { got = name; return nil }
	}
	detachOld := link.attach(sender("old"))
	detachNew := link.attach(sender("new"))
	detachOld()
	if err := link.write(context.Background(), nil); err != nil || got != "new" {
		t.Fatalf("got %q, %v", got, err)
	}
	detachNew()
	if err := link.write(context.Background(), nil); !errors.Is(err, ErrNoStation) {
		t.Fatal(err)
	}
}

// The push must reach the Station over the real socket and never go upstream.
func TestVolumeHandlerReachesConnectedStation(t *testing.T) {
	upstreamGot := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err == nil {
			upstreamGot <- struct{}{}
		}
	}))
	defer upstream.Close()
	p := &Proxy{Upstream: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/uni.ws"}
	call := func(query string) int {
		w := httptest.NewRecorder()
		p.VolumeHandler(w, httptest.NewRequest(http.MethodGet, "/volume?"+query, nil))
		return w.Code
	}
	if code := call("level=4"); code != http.StatusServiceUnavailable {
		t.Fatalf("no station: %d", code)
	}
	if code := call("level=abc"); code != http.StatusBadRequest {
		t.Fatalf("bad level: %d", code)
	}
	server := httptest.NewServer(p)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	station, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer station.CloseNow()
	for call("level=4") != http.StatusOK {
		if ctx.Err() != nil {
			t.Fatal("station never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, data, err := station.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertVolumePush(t, data, 4)
	select {
	case <-upstreamGot:
		t.Fatal("push leaked upstream")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRetryCommandAcceptsBothPhrases(t *testing.T) {
	for text, want := range map[string]bool{"используй нейросеть": true, "Алиса, спроси нейросеть": true, "спроси нейросеть почему небо голубое": false} {
		if isRetryCommand(text) != want {
			t.Fatalf("%q", text)
		}
	}
}
