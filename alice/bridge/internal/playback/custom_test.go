package playback

import "testing"

func TestCustomWordsExtendBuiltIns(t *testing.T) {
	SetCustom(Custom{Spotify: []string{"Шпотифай"}, YouTube: []string{"ютубчик"}, Relay: []string{"крикни в"}, Commands: map[string][]string{"pause": {"замолчи"}}})
	defer custom.Store(nil)
	if _, query, provider, ok := Search("Алиса, включи Кино в шпотифай"); !ok || provider != "spotify" || query != "кино" {
		t.Fatalf("spotify tail: %q %q %v", query, provider, ok)
	}
	if _, query, provider, ok := Search("включи через ютубчик Queen"); !ok || provider != "youtube" || query != "queen" {
		t.Fatalf("youtube head: %q %q %v", query, provider, ok)
	}
	if room, message, ok := Relay("крикни в кухню ужин готов"); !ok || room != "кухню" || message != "ужин готов" {
		t.Fatalf("relay: %q %q", room, message)
	}
	if Command("Замолчи!") != "pause" || Command("пауза") != "pause" {
		t.Fatal("pause words")
	}
	if _, _, provider, _ := Search("включи кино в спотифай"); provider != "spotify" {
		t.Fatal("built-in name lost")
	}
}

func TestServerBaseReplacesBuiltIns(t *testing.T) {
	SetCustom(Custom{Replace: true, Spotify: []string{"спотифа*"}, YouTube: []string{"ютуб*"}, Relay: []string{"передай в"}, Commands: map[string][]string{"pause": {"пауза"}, "stop": {"хватит"}}})
	defer custom.Store(nil)
	if _, _, provider, _ := Search("включи кино в ютубе"); provider != "youtube" {
		t.Fatal("stem lost")
	}
	if _, _, provider, _ := Search("включи кино в spotify"); provider != "" {
		t.Fatal("switched-off name still heard")
	}
	if Command("стоп") != "" || Command("хватит") != "stop" || Command("начни сначала") != "restart" {
		t.Fatal("commands must follow the base, restart stays")
	}
	if _, _, ok := Relay("скажи на кухню привет"); ok {
		t.Fatal("switched-off intercom word still heard")
	}
	if room, _, ok := Relay("передай в кухню привет"); !ok || room != "кухню" {
		t.Fatal("intercom word from the base")
	}
}
