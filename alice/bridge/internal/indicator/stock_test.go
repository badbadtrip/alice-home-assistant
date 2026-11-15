package indicator

import (
	"slices"
	"testing"
)

func TestMountedFindsOldPatternCopies(t *testing.T) {
	mounts := "tmpfs /tmp/alice-ring/ring /system/vendor/quasar/ledpatterns/ring tmpfs rw 0 0\n/dev/root / ext4 ro 0 0\n"
	if got := Mounted(mounts); !slices.Equal(got, []string{"/system/vendor/quasar/ledpatterns/ring"}) {
		t.Fatalf("got %v", got)
	}
	if got := Mounted("/dev/root / ext4 ro 0 0\n"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
