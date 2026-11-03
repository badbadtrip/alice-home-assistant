package home

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"quasarbridge/internal/dialogue"
	"quasarbridge/internal/llm"
	"strings"
	"testing"
)

type clarificationPlanner struct {
	turn int
	t    *testing.T
}

func (p *clarificationPlanner) Complete(_ context.Context, m []llm.Message, _ bool) (string, error) {
	p.turn++
	if p.turn == 1 {
		return `{"answer":"Какой свет проверить?","calls":[],"clarify":true}`, nil
	}
	if !strings.Contains(m[1].Content, `"previous_request":"проверь свет"`) || !strings.Contains(m[1].Content, `"request":"на кухне"`) {
		p.t.Error("clarification context missing")
	}
	return `{"answer":"Свет выключен.","calls":[],"clarify":false}`, nil
}
func TestFunctionsClarificationContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" { w.Write([]byte(`{"matched":false}`)); return }
		if r.Method != http.MethodGet {
			t.Error("read-only clarification invoked an action")
		}
		if r.URL.Path == "/functions" {
			w.Write([]byte(`{"light":{"arguments":{},"description":"light"}}`))
		} else {
			w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	f := Functions{URL: server.URL, Planner: &clarificationPlanner{t: t}}
	ctx := dialogue.WithSpeaker(t.Context(), "speaker-one")
	if _, err := f.Handle(ctx, "проверь свет"); err != nil {
		t.Fatal(err)
	}
	if !f.HasPending(ctx) {
		t.Fatal("clarification not retained")
	}
	if f.HasPending(dialogue.WithSpeaker(t.Context(), "speaker-two")) {
		t.Fatal("clarification leaked between speakers")
	}
	if _, err := f.Handle(ctx, "на кухне"); err != nil {
		t.Fatal(err)
	}
	if f.HasPending(ctx) {
		t.Fatal("resolved clarification still pending")
	}
}

type functionPlanner string

type sensorOnlyPlanner struct{ t *testing.T }

func (p sensorOnlyPlanner) Complete(_ context.Context, messages []llm.Message, _ bool) (string, error) {
	if !strings.Contains(messages[1].Content, "42, g") {
		p.t.Error("sensor value not supplied to planner")
	}
	return `{"answer":"Кот съел 42 грамма.","calls":[],"clarify":false}`, nil
}
func TestFunctionsWithOnlyReadOnlySensors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" { w.Write([]byte(`{"matched":false}`)); return }
		if r.Method != http.MethodGet {
			t.Error("sensor reading attempted a service call")
		}
		if r.URL.Path == "/functions" {
			w.Write([]byte(`{}`))
		} else {
			w.Write([]byte(`{"current_sensor_feeder_weight_per_day_state":{"description":"Сколько кот скушал","value":"42, g"}}`))
		}
	}))
	defer server.Close()
	f := Functions{URL: server.URL, Planner: sensorOnlyPlanner{t}}
	answer, err := f.Handle(t.Context(), "сколько кот съел сегодня")
	if err != nil || answer != "Кот съел 42 грамма." {
		t.Fatalf("answer=%s error=%v", answer, err)
	}
}

func (p functionPlanner) Complete(context.Context, []llm.Message, bool) (string, error) {
	return string(p), nil
}
func TestFunctionsPlanValidation(t *testing.T) {
	for _, tc := range []struct {
		name, plan string
		wantCalls  int
		wantError  bool
	}{
		{"unknown", `{"answer":"","calls":[{"name":"invented","arguments":{}}]}`, 0, true},
		{"range", `{"answer":"","calls":[{"name":"set_light_state","arguments":{"state":2}}]}`, 0, true},
		{"missing", `{"answer":"","calls":[{"name":"set_light_state","arguments":{}}]}`, 0, true},
		{"extra", `{"answer":"","calls":[{"name":"set_light_state","arguments":{"state":1,"extra":1}}]}`, 0, true},
		{"whole plan", `{"answer":"","calls":[{"name":"set_light_state","arguments":{"state":1}},{"name":"invented","arguments":{}}]}`, 0, true},
		{"readonly", `{"answer":"Свет выключен.","calls":[]}`, 0, false},
		{"valid", `{"answer":"","calls":[{"name":"set_light_state","arguments":{"state":1}}]}`, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing authorization")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/functions":
					if r.Method == http.MethodPost {
						calls++
						w.Write([]byte(`{}`))
						return
					}
					w.Write([]byte(`{"set_light_state":{"description":"light","arguments":{"type":"object","properties":{"state":{"anyOf":[{"const":0},{"const":1}]}},"required":["state"]}}}`))
				case "/state":
					json.NewEncoder(w).Encode(map[string]any{"light": "off"})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			f := Functions{URL: server.URL, Token: "test-token", Planner: functionPlanner(tc.plan)}
			_, err := f.Handle(context.Background(), "включи свет")
			if (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if calls != tc.wantCalls {
				t.Fatalf("physical calls: %d", calls)
			}
		})
	}
}
func TestFunctionsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"response":"done"}`))
	}))
	defer server.Close()
	f := Functions{URL: server.URL}
	if _, err := f.Catalog(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatal("HTTP failure accepted", err)
	}
}
