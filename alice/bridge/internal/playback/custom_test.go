package playback

import "testing"

func TestCustomWordsExtendBuiltIns(t *testing.T) {
	SetCustom(Custom{Relay: []string{"крикни в"}, Commands: map[string][]string{"pause": {"замолчи"}}})
	defer custom.Store(nil)
	if room, message, ok := Relay("крикни в кухню ужин готов"); !ok || room != "кухню" || message != "ужин готов" {
		t.Fatalf("relay: %q %q", room, message)
	}
	if Command("Замолчи!") != "pause" || Command("пауза") != "pause" {
		t.Fatal("pause words")
	}
}

func TestServerBaseReplacesBuiltIns(t *testing.T) {
	SetCustom(Custom{Replace: true, Relay: []string{"передай в"}, Commands: map[string][]string{"pause": {"пауза"}, "stop": {"хватит"}}})
	defer custom.Store(nil)
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
