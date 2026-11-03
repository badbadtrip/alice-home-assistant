package home

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"quasarbridge/internal/sound"
	"strings"
	"testing"
)

func TestExactHomePlanAndExecutionStatuses(t *testing.T) {
	for _, status := range []string{"confirmed", "already", "accepted", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/resolve":
					var data map[string]string
					json.NewDecoder(r.Body).Decode(&data)
					if data["area"] != "Кухня" {
						t.Error("speaker area missing")
					}
					w.Write([]byte(`{"matched":true,"calls":[{"name":"set_light_state","arguments":{"state":0}}]}`))
				case "/functions":
					if r.Method == http.MethodPost {
						calls++
						json.NewEncoder(w).Encode(map[string]any{"execution": map[string]string{"name": "Свет", "status": status, "target_state": "off"}})
					} else {
						w.Write([]byte(`{"set_light_state":{"arguments":{"type":"object","properties":{"state":{"anyOf":[{"const":0},{"const":1}]}},"required":["state"]}}}`))
					}
				default:
					t.Error("exact command unexpectedly fetched state or called planner")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			f := Functions{URL: server.URL, Area: "Кухня"}
			answer, err := f.Handle(t.Context(), "выключи свет здесь")
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			expected := map[string]string{"confirmed": "выключено", "already": "уже выключено", "accepted": "пока не подтверждён", "unavailable": "команда не отправлена"}[status]
			if !strings.Contains(answer, expected) {
				t.Fatalf("unexpected answer %q", answer)
			}
			chimes := 0
			ctx := sound.WithPlayer(t.Context(), func(_ context.Context, r sound.Request) error {
				if r.Sound != "success" {
					t.Error("wrong sound")
				}
				chimes++
				return nil
			})
			answer, err = f.Handle(ctx, "выключи свет здесь")
			if err != nil {
				t.Fatal(err)
			}
			if status == "confirmed" || status == "already" || status == "accepted" {
				if answer != "" || chimes != 1 {
					t.Fatalf("success must chime once without speech: %q, %d", answer, chimes)
				}
			} else if answer == "" || chimes != 0 {
				t.Fatalf("unavailable must retain spoken explanation: %q, %d", answer, chimes)
			}
		})
	}
}
