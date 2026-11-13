package home

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"quasarbridge/internal/telemetry"
	"testing"
	"time"
)

func TestActivityUsesBoundedAuthenticatedEventsTransport(t *testing.T) {
	received := make(chan telemetry.Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/events" || r.Header.Get("Authorization") != "Bearer test" || r.Header.Get("X-Alice-Speaker") != "midi" {
			t.Error("wrong telemetry transport")
		}
		var event telemetry.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		received <- event
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	functions := &Functions{URL: server.URL, Token: "test", SpeakerID: "midi"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	functions.StartActivity(ctx)
	_, trace := telemetry.Begin(ctx, "проверь свет", "home", "rule")
	functions.RecordActivity(trace.Finish("home", nil, true))
	select {
	case event := <-received:
		if event.SpeakerID != "midi" || event.Status != "answered" {
			t.Fatal("wrong recorded metadata")
		}
	case <-time.After(time.Second):
		t.Fatal("event not sent")
	}
}
