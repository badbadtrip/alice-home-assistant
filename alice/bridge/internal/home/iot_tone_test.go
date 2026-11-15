package home

import "testing"

func TestIoTTone(t *testing.T) {
	for targets, want := range map[string]string{"on": "iot_on", "off": "iot_off", "on,off": "iot_done", "": "iot_done", "set": "iot_done"} {
		m := map[string]bool{}
		for _, k := range splitTargets(targets) {
			m[k] = true
		}
		if got := iotTone(m); got != want {
			t.Errorf("%q: got %s, want %s", targets, got, want)
		}
	}
}

func splitTargets(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
