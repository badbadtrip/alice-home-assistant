package ytm

import "testing"

func TestCurrentCleansVideoArtistAndIgnoresStopped(t *testing.T) {
	s := state{Status: "playing", Index: 1, PositionMS: 5000, Queue: []track{{Title: "a"}, {Title: "Утро", Artists: "Дайте танк (!) • 16 млн просмотров • Отметок", DurationMS: 161000, Thumbnail: "https://i.ytimg.com/x.jpg"}}}
	got := current(s)
	if got == nil || got.Source != "youtube_music" || got.Title != "Утро" || got.Artist != "Дайте танк (!)" || got.Cover != "https://i.ytimg.com/x.jpg" || got.DurationMS != 161000 || got.PositionMS != 5000 {
		t.Fatalf("got %+v", got)
	}
	s.Status = "stopped"
	if current(s) != nil {
		t.Fatal("a stopped player kept its queue as current")
	}
	s.Status, s.Index = "paused", 9
	if got := current(s); got == nil || got.Title != "" {
		t.Fatalf("got %+v", got)
	}
}
