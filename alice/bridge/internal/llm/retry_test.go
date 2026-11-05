package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetriesOnceThenStripsFence(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + "```json {\\\"a\\\":1} ```" + `"}}]}`))
	}))
	defer server.Close()
	answer, err := (&Client{BaseURL: server.URL}).Complete(context.Background(), nil, true)
	if err != nil || answer != `{"a":1}` || calls != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	if _, err := (&Client{BaseURL: server.URL}).Complete(context.Background(), nil, true); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
