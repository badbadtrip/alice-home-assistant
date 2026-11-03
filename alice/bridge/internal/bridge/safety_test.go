package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quasarbridge/internal/llm"
)

func TestQuestionSafetyFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		question string
	}{
		{"missing_model", t.Context(), "Почему небо голубое?"},
		{"empty_question", t.Context(), ""},
		{"canceled", ctx, "Почему небо голубое?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if safe, err := (&Engine{}).CheckQuestionSafety(tc.ctx, QuestionSafetyInput{Question: tc.question}); safe || err == nil {
				t.Fatal("missing or invalid safety decision must fail closed")
			}
		})
	}
}

func TestQuestionSafetyRequiresExplicitDecision(t *testing.T) {
	for _, tc := range []struct {
		decision string
		want     bool
	}{
		{`{"safe":true}`, true},
		{`{"safe":false}`, false},
		{`{}`, false},
		{`{"safe":true,"unexpected":true}`, false},
		{`not JSON`, false},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Encode only synthetic classifier text as the real API envelope.
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": tc.decision}, "finish_reason": "stop"}}})
			}))
			defer srv.Close()
			e := &Engine{LLM: &llm.Client{BaseURL: srv.URL, Model: "synthetic-guard"}}
			safe, _ := e.CheckQuestionSafety(t.Context(), QuestionSafetyInput{Question: "Почему небо голубое?"})
			if safe != tc.want {
				t.Fatalf("safe=%v want=%v", safe, tc.want)
			}
		})
	}
}
