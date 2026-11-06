package soloist

import (
	"encoding/json/v2"
	"testing"
)

func TestCurrentReadsLiveEvent(t *testing.T) {
	var event playback
	data := `{"type":"playback_state","status":"paused","item":{"decorations":{"identity":{"name":"Мама, я люблю"},"visual_identity":{"cover":[{"url":"s","size":"small"},{"url":"l","size":"large"},{"url":"x","size":"xlarge"}]},"parent":{"entity":{"decorations":{"identity":{"name":"Байки инсайдера"}}}},"creators":[{"entity":{"decorations":{"identity":{"name":"Anacondaz"}}}}],"playback":{"duration_ms":225080}}},"position":{"position_ms":8289}}`
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatal(err)
	}
	got := current(event)
	if got == nil || got.Source != "spotify" || got.Status != "paused" || got.Title != "Мама, я люблю" || got.Artist != "Anacondaz" || got.Album != "Байки инсайдера" || got.Cover != "l" || got.PositionMS != 8289 || got.DurationMS != 225080 {
		t.Fatalf("got %+v", got)
	}
	if current(playback{Status: "stopped"}) != nil {
		t.Fatal("stopped is not playing")
	}
}
