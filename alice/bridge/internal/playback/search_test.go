package playback

import "testing"

func TestSearch(t *testing.T) {
	cases := []struct{ text, kind, query string }{
		{"Алиса, включи песню Группа крови", "track", "группа крови"},
		{"поставь мне группу Кино", "artist", "кино"},
		{"включи альбом Meteora", "album", "meteora"},
		{"включи плейлист для бега", "playlist", "для бега"},
		{"включи мои лайки", "liked", ""},
	}
	for _, c := range cases {
		kind, query, ok := Search(c.text)
		if !ok || kind != c.kind || query != c.query {
			t.Errorf("%q: got %q %q %v", c.text, kind, query, ok)
		}
	}
	for _, text := range []string{"включи кино", "включи свет", "включи музыку", "какая песня"} {
		if _, _, ok := Search(text); ok {
			t.Errorf("%q must stay outside search", text)
		}
	}
}
