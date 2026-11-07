package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLANHandlerAllowsOnlyRoutineCallsWithToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := lanHandler("secret", inner)
	cases := []struct {
		path, token string
		want        int
	}{
		{"/healthz", "", http.StatusUnauthorized},
		{"/healthz", "wrong", http.StatusUnauthorized},
		{"/healthz", "secret", http.StatusNoContent},
		{"/player", "secret", http.StatusNoContent},
		{"/uni.ws", "secret", http.StatusNotFound},
	}
	for _, c := range cases {
		request := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.token != "" {
			request.Header.Set("Authorization", "Bearer "+c.token)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != c.want {
			t.Errorf("%s %q: got %d", c.path, c.token, recorder.Code)
		}
	}
}
