// Package indicator gives the LED ring back to the firmware.
//
// Up to 0.65 the bridge drew its own ring looks and hid the firmware's voice and player
// animations behind dark copies bind-mounted over the pattern directories. The ring is
// now the firmware's alone; this package only undoes that mount on speakers that still
// have it from an older bridge.
package indicator

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var stockPatternDirs = []string{"/system/vendor/quasar/ledpatterns/ring", "/system/vendor/quasar/ledpatterns/oknix"}

// Mounted tells which pattern directories are still covered by an old bridge's copy.
func Mounted(mounts string) []string {
	var dirs []string
	for _, dir := range stockPatternDirs {
		if strings.Contains(mounts, " "+dir+" ") {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// RestoreStock unmounts the old copies and removes them. The firmware reads its
// patterns when it starts, so the stock look may need the next reboot to come back.
func RestoreStock() error {
	mounts, _ := os.ReadFile("/proc/mounts")
	var failed error
	for _, dir := range Mounted(string(mounts)) {
		if err := exec.Command("umount", dir).Run(); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	if failed == nil {
		_ = os.RemoveAll("/tmp/alice-ring")
	}
	for _, name := range []string{"indicator.json", "indicator-ready"} {
		_ = os.Remove(filepath.Join("/data/opt/quasar-bridge", name))
	}
	return failed
}
