package ytm

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

func server(t *testing.T, answer string, seen chan<- map[string]string) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skip("no unix sockets here")
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request map[string]string
			_ = json.NewDecoder(conn).Decode(&request)
			seen <- request
			_, _ = conn.Write([]byte(answer))
			conn.Close()
		}
	}()
	return &Client{Root: t.TempDir(), Socket: path}
}

func TestActiveFollowsStatus(t *testing.T) {
	seen := make(chan map[string]string, 1)
	if !server(t, `{"state":{"status":"paused"}}`, seen).Active(context.Background()) {
		t.Fatal("paused must stay active")
	}
	if (<-seen)["action"] != "status" {
		t.Fatal("wrong action")
	}
}

func TestPlayReportsPlayerError(t *testing.T) {
	seen := make(chan map[string]string, 1)
	if err := server(t, `{"error":"нет"}`, seen).Play(context.Background(), "кино"); err == nil {
		t.Fatal("error lost")
	}
	if request := <-seen; request["action"] != "play" || request["query"] != "кино" {
		t.Fatalf("got %v", request)
	}
}

func TestEmptyPlayAsksForRecommendations(t *testing.T) {
	seen := make(chan map[string]string, 1)
	_ = server(t, `{}`, seen).Play(context.Background(), "")
	if (<-seen)["action"] != "recommendations" {
		t.Fatal("wrong action")
	}
}
