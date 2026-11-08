//go:build linux

package localstate

import (
	"context"
	"os"
	"syscall"
	"unsafe"
)

// SNDRV_CTL_IOCTL_SUBSCRIBE_EVENTS = _IOWR('U', 0x16, int).
const subscribeEvents = 0xC0045516

// mixerEvents signals every ALSA control change on card 0. The kernel queues a small
// event per change, so watching costs nothing while the volume stays put.
func mixerEvents(ctx context.Context) <-chan struct{} {
	file, err := os.OpenFile("/dev/snd/controlC0", os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	on := int32(1)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), subscribeEvents, uintptr(unsafe.Pointer(&on))); errno != 0 {
		file.Close()
		return nil
	}
	events := make(chan struct{}, 8)
	go func() { <-ctx.Done(); file.Close() }()
	go func() {
		defer close(events)
		buffer := make([]byte, 1024) // whole struct snd_ctl_event records
		for {
			if _, err := file.Read(buffer); err != nil {
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	return events
}
