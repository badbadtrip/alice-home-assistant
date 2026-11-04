package llm

import (
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestRequestIndicatorReleasedOnHTTPFailure(t *testing.T) {
 active, starts := 0, 0
 s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  if active != 1 { t.Error("request was not indicated") }
  http.Error(w, "unavailable", 503)
 }))
 defer s.Close()
 c := Client{BaseURL:s.URL, OnRequest:func()func(){active++; starts++; return func(){active--}}}
 _, err := c.Complete(t.Context(), nil, false)
 if err == nil || active != 0 || starts != 1 { t.Fatalf("error=%v active=%d starts=%d",err,active,starts) }
 c.BaseURL = ":bad url"
 _, _ = c.Complete(t.Context(), nil, false)
 if active != 0 || starts != 1 { t.Fatal("invalid endpoint claimed the LEDs") }
}
