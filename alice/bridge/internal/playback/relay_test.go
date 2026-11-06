package playback

import "testing"

func TestRelay(t *testing.T) {
	room, message, ok := Relay("Алиса, скажи на кухню, скоро там кушать будет?")
	if !ok || RoomStem(room) != "кухн" || message != "скоро там кушать будет" {
		t.Fatalf("got %q %q %v", room, message, ok)
	}
	if _, message, _ := Relay("передай в комнату что ужин готов"); message != "ужин готов" {
		t.Fatalf("got %q", message)
	}
	for _, text := range []string{"скажи анекдот", "скажи который час", "включи на кухне свет"} {
		if _, _, ok := Relay(text); ok {
			t.Errorf("%q is not a relay", text)
		}
	}
}
