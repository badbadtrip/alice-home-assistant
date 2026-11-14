package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	airplay "github.com/skrashevich/station-airplay"
)

type fakeReceiver struct {
	starts, stops int
	failures      int
	// status replaces the default snapshot when set.
	status *airplay.Status
}

func (f *fakeReceiver) Start(context.Context) error {
	f.starts++
	if f.failures > 0 {
		f.failures--
		return errors.New("not ready")
	}
	return nil
}
func (f *fakeReceiver) Stop(context.Context) error { f.stops++; return nil }
func (f *fakeReceiver) Status() airplay.Status {
	if f.status != nil {
		return *f.status
	}
	return airplay.Status{State: airplay.StateRunning, Name: "Алиса Кухня", Connected: true}
}

func testAirPlay(t *testing.T, f *fakeReceiver) *airPlayControl {
	t.Helper()
	return &airPlayControl{receiver: f, app: context.Background(), marker: filepath.Join(t.TempDir(), "airplay.enabled"), retry: time.Millisecond}
}

func airPlayCall(c *airPlayControl, action string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/airplay?action="+action, nil))
	return w
}

func TestAirPlayChoiceSurvivesRestart(t *testing.T) {
	f := &fakeReceiver{}
	c := testAirPlay(t, f)
	c.resume()
	if f.starts != 0 {
		t.Fatal("receiver started without a saved choice")
	}
	if w := airPlayCall(c, "start"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	f.starts = 0
	f.failures = 2
	c.resume()
	if f.starts != 3 {
		t.Fatalf("boot retries: %d", f.starts)
	}
	if w := airPlayCall(c, "stop"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) || f.stops != 1 {
		t.Fatalf("%d %s stops=%d", w.Code, w.Body, f.stops)
	}
}

func TestAirPlayFailedStartSavesNothing(t *testing.T) {
	c := testAirPlay(t, &fakeReceiver{failures: 1})
	if w := airPlayCall(c, "start"); w.Code != http.StatusBadGateway || c.enabled() {
		t.Fatalf("%d enabled=%v", w.Code, c.enabled())
	}
	if w := airPlayCall(c, "reboot"); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
}

func TestAirPlayFollowsNewAddress(t *testing.T) {
	f := &fakeReceiver{status: &airplay.Status{State: airplay.StateRunning, Address: "192.168.1.59:7000"}}
	c := testAirPlay(t, f)
	if w := airPlayCall(c, "start"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.app = ctx
	addresses := make(chan string, 4)
	addresses <- "192.168.1.59" // unchanged: nothing happens
	addresses <- "192.168.1.77" // changed: one restart
	current := func() string {
		select {
		case a := <-addresses:
			return a
		default:
			cancel()
			return ""
		}
	}
	c.followAddress(time.Millisecond, current)
	if f.stops != 1 || f.starts != 2 {
		t.Fatalf("stops=%d starts=%d", f.stops, f.starts)
	}
}

func TestAirPlayKeepsSessionOnNewAddress(t *testing.T) {
	f := &fakeReceiver{status: &airplay.Status{State: airplay.StateRunning, Address: "192.168.1.59:7000", Connected: true}}
	c := testAirPlay(t, f)
	airPlayCall(c, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c.app = ctx
	c.followAddress(time.Millisecond, func() string { return "192.168.1.77" })
	if f.stops != 0 {
		t.Fatal("restarted while a sender owns playback")
	}
}
