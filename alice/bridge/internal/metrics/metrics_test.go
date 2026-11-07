package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSystemReadsProcAndHottestZone(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "proc/uptime"), "176461.52 600000.00\n")
	write(t, filepath.Join(dir, "proc/loadavg"), "2.41 2.16 2.10 1/380 26584\n")
	write(t, filepath.Join(dir, "proc/meminfo"), "MemTotal:  243492 kB\nMemFree: 28964 kB\nMemAvailable: 105324 kB\n")
	write(t, filepath.Join(dir, "sys/class/thermal/thermal_zone0/temp"), "45000\n")
	write(t, filepath.Join(dir, "sys/class/thermal/thermal_zone1/temp"), "52700\n")
	s := Root{Proc: filepath.Join(dir, "proc"), Sys: filepath.Join(dir, "sys"), Data: dir}.System()
	if s.UptimeS != 176461 || s.Load1 != 2.41 || s.MemTotalKB != 243492 || s.MemAvailableKB != 105324 {
		t.Fatalf("got %+v", s)
	}
	if s.TempC == nil || *s.TempC != 52.7 {
		t.Fatalf("temperature %v", s.TempC)
	}
}

func TestRatesFollowCounters(t *testing.T) {
	dir := t.TempDir()
	root := Root{Proc: filepath.Join(dir, "proc"), Sys: dir, Data: dir}
	write(t, filepath.Join(dir, "proc/stat"), "cpu  100 0 100 700 100 0 0 0\n")
	write(t, filepath.Join(dir, "proc/net/dev"), `x|
 y|
    lo: 999 0 0 0 0 0 0 0 999
 wlan0: 1000 0 0 0 0 0 0 0 500
`)
	if s := root.System(); s.CPUPercent != nil {
		t.Fatal("first reading has no rate")
	}
	write(t, filepath.Join(dir, "proc/stat"), "cpu  150 0 150 700 100 0 0 0\n")
	s := root.System()
	if s.CPUPercent == nil || *s.CPUPercent != 100 {
		t.Fatalf("cpu %v", s.CPUPercent)
	}
	if s.NetRxBps == nil || *s.NetRxBps != 0 {
		t.Fatalf("rx %v", s.NetRxBps)
	}
}

func TestHandlerNeedsTheToken(t *testing.T) {
	handler := Handler("secret", func(context.Context) Snapshot { return Snapshot{Status: "ok"} })
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", recorder.Code)
	}
	request.Header.Set("Authorization", "Bearer secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("token: %d", recorder.Code)
	}
}
