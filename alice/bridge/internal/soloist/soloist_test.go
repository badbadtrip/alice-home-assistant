package soloist

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func server(t *testing.T, status string, seen chan<- string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			seen <- string(data)
			if strings.Contains(string(data), "get_queue") && status == "queued" {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"queue_changed","upcoming":[{"source":"queue"},{"source":"context"}]}`))
			}
			if strings.Contains(string(data), "get_state") {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"playback_state","status":"`+status+`"}`))
			}
		}
	}))
	t.Cleanup(s.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(s.URL, "http://"))
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ws.addr"), []byte(host+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ws.port"), []byte(port+"\n"), 0o600)
	return &Client{StateDir: dir}
}

func TestActiveFollowsPlaybackState(t *testing.T) {
	seen := make(chan string, 8)
	if !server(t, "playing", seen).Active(context.Background()) {
		t.Fatal("playing is active")
	}
	if server(t, "stopped", seen).Active(context.Background()) {
		t.Fatal("stopped is not active")
	}
}

func TestControlSendsCommand(t *testing.T) {
	seen := make(chan string, 8)
	c := server(t, "playing", seen)
	if _, err := c.Control(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; !strings.Contains(got, "skip_next") {
		t.Fatal(got)
	}
	if _, err := c.Control(context.Background(), "restart"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestRemoteEndpointRejected(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ws.addr"), []byte("192.168.1.2"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ws.port"), []byte("1"), 0o600)
	if _, err := (&Client{StateDir: dir}).endpoint(); err == nil {
		t.Fatal("remote endpoint accepted")
	}
}

func TestPlayURISendsPlaylist(t *testing.T) {
	seen := make(chan string, 8)
	if _, err := server(t, "stopped", seen).PlayURI(context.Background(), "spotify:playlist:abc"); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; !strings.Contains(got, "get_queue") {
		t.Fatalf("queue check: %s", got)
	}
	if got := <-seen; !strings.Contains(got, "activate") {
		t.Fatalf("first command: %s", got)
	}
	if got := <-seen; !strings.Contains(got, `"uri":"spotify:playlist:abc"`) || !strings.Contains(got, `"command":"play"`) {
		t.Fatalf("play command: %s", got)
	}
}

func TestPlayURISkipsLeftoverQueue(t *testing.T) {
	seen := make(chan string, 8)
	if _, err := server(t, "queued", seen).PlayURI(context.Background(), "spotify:artist:abc"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"get_queue", "activate", "skip_next", `"command":"play"`} {
		if got := <-seen; !strings.Contains(got, want) {
			t.Fatalf("want %s, got %s", want, got)
		}
	}
}

func TestAvailableBeforeServiceStarts(t *testing.T) {
	if !(&Client{StateDir: t.TempDir()}).Available() {
		t.Fatal("installed service must count before its endpoint exists")
	}
	if (&Client{StateDir: filepath.Join(t.TempDir(), "missing")}).Available() {
		t.Fatal("speaker without Soloist must not count")
	}
}
