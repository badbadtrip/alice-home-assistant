package playback

import "testing"

func TestSearch(t *testing.T) {
	cases := []struct{ text, kind, query, provider string }{
		{"Алиса, включи песню Группа крови", "track", "группа крови", ""},
		{"поставь мне группу Кино", "artist", "кино", ""},
		{"включи альбом Meteora в спотифай", "album", "meteora", "spotify"},
		{"включи плейлист для бега", "playlist", "для бега", ""},
		{"включи мои лайки", "liked", "", ""},
		{"включи Кино в спотифай", "any", "кино", "spotify"},
		{"включи через ютуб Queen", "any", "queen", "youtube"},
		{"включи Queen в ютубе", "any", "queen", "youtube"},
		{"поставь в ютуб музыке песню Numb", "track", "numb", "youtube"},
		{"включи мои лайки на ютубе", "liked", "", "youtube"},
		{"сыграй Кино из спотифая", "any", "кино", "spotify"},
	}
	for _, c := range cases {
		kind, query, provider, ok := Search(c.text)
		if !ok || kind != c.kind || query != c.query || provider != c.provider {
			t.Errorf("%q: got %q %q %q %v", c.text, kind, query, provider, ok)
		}
	}
	for _, text := range []string{"включи кино", "включи свет", "включи музыку", "какая песня"} {
		if _, _, _, ok := Search(text); ok {
			t.Errorf("%q must stay outside search", text)
		}
	}
}
