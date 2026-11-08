package indicator

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The firmware animates listening, thinking, speaking and the player buttons from these
// pattern files. It loads them once when it starts, and the bridge starts first at boot.
var stockPatternDirs = []string{"/system/vendor/quasar/ledpatterns/ring", "/system/vendor/quasar/ledpatterns/oknix"}

// stockReplaced lists the animations the bridge draws itself when the owner chose an own look.
// Errors, timers, calls and network setup keep the firmware's patterns.
func stockReplaced(name string) bool {
	return strings.HasSuffix(name, ".led") && (strings.HasPrefix(name, "alice_") || strings.HasPrefix(name, "player_") || name == "intonation_spotting.led")
}

// darkPattern is one black frame for as many LEDs as the original has. A missing file
// might upset the firmware; a dark one just draws nothing.
func darkPattern(original []byte) []byte {
	scanner := bufio.NewScanner(strings.NewReader(string(original)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] == "loop" {
			continue
		}
		return []byte(strings.Repeat("000000 ", len(fields)-1) + "20\n")
	}
	return []byte("000000 20\n")
}

// ReplaceStock hides the firmware's voice and player animations behind a copy in RAM, so
// only the bridge's look shows. It changes nothing on flash, and a reboot without the
// bridge brings the stock ring back. root is "" on the speaker.
func ReplaceStock(root string) error {
	mounts, _ := os.ReadFile(filepath.Join(root, "/proc/mounts"))
	var failed error
	for _, dir := range stockPatternDirs {
		source := filepath.Join(root, dir)
		entries, err := os.ReadDir(source)
		if err != nil || strings.Contains(string(mounts), " "+dir+" ") {
			continue
		}
		copyDir := filepath.Join(root, "/tmp/alice-ring", filepath.Base(dir))
		if err := os.MkdirAll(copyDir, 0o755); err != nil {
			failed = err
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
			if err != nil {
				failed = err
				continue
			}
			if stockReplaced(entry.Name()) {
				raw = darkPattern(raw)
			}
			if err := os.WriteFile(filepath.Join(copyDir, entry.Name()), raw, 0o644); err != nil {
				failed = err
			}
		}
		if root == "" {
			if err := exec.Command("mount", "--bind", copyDir, dir).Run(); err != nil {
				failed = errors.Join(failed, err)
			}
		}
	}
	return failed
}

// StockReplaced tells whether the firmware's voice animations are hidden on this boot.
func StockReplaced() bool {
	mounts, _ := os.ReadFile("/proc/mounts")
	for _, dir := range stockPatternDirs {
		if strings.Contains(string(mounts), " "+dir+" ") {
			return true
		}
	}
	return false
}
