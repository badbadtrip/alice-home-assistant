package indicator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceStockDarkensOnlyVoiceAndPlayer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "/system/vendor/quasar/ledpatterns/oknix")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "alice_listening.led"), "loop\n6b0fff 6b0fff 6b0fff 40\n")
	write(t, filepath.Join(dir, "player_pause.led"), "e800f3 e800f3 e800f3 240\n")
	write(t, filepath.Join(dir, "error.led"), "ff0000 ff0000 ff0000 40\n")
	if err := ReplaceStock(root); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		raw, _ := os.ReadFile(filepath.Join(root, "/tmp/alice-ring/oknix", name))
		return string(raw)
	}
	if got := read("alice_listening.led"); got != "000000 000000 000000 20\n" {
		t.Fatalf("listening: %q", got)
	}
	if got := read("player_pause.led"); got != "000000 000000 000000 20\n" {
		t.Fatalf("pause: %q", got)
	}
	if got := read("error.led"); got != "ff0000 ff0000 ff0000 40\n" {
		t.Fatalf("error must stay stock: %q", got)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
