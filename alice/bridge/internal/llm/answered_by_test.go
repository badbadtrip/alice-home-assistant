package llm

import "testing"

func TestAnsweredByPrefersReplyOverAlias(t *testing.T) {
	c := &Client{Model: "alice-gemini"}
	if got := c.answeredBy([]byte(`{"model":"deepseek-chat"}`)); got != "deepseek-chat" {
		t.Fatal(got)
	}
	if got := c.answeredBy([]byte(`{"choices":[]}`)); got != "alice-gemini" {
		t.Fatal(got)
	}
}
