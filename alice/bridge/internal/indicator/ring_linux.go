//go:build linux

package indicator

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	fbGetVar = 0x4600
	fbPan    = 0x4606
)

// ring writes frames straight to the LED framebuffer. The firmware paints through
// the same buffer, so nothing has to be taken over or restored afterwards.
type ring struct {
	file *os.File
	size int
	pan  bool
	info [160]byte
}

func openRing() (*ring, error) {
	raw, err := os.ReadFile("/sys/class/graphics/fb0/virtual_size")
	if err != nil {
		return nil, err
	}
	var width, height int
	if _, err = fmt.Sscanf(strings.TrimSpace(string(raw)), "%d,%d", &width, &height); err != nil || width < 1 || width > 64 || height != 1 {
		return nil, errors.New("unknown LED framebuffer")
	}
	name, _ := os.ReadFile("/sys/class/graphics/fb0/name")
	file, err := os.OpenFile("/dev/fb0", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	r := &ring{file: file, size: width}
	// MIDI's driver shows a write only on the next pan.
	if strings.TrimSpace(string(name)) == "ring" {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), fbGetVar, uintptr(unsafe.Pointer(&r.info))); errno != 0 {
			file.Close()
			return nil, errno
		}
		r.pan = true
	}
	return r, nil
}

func (r *ring) show(frame []Pixel) error {
	data := make([]byte, 0, len(frame)*3)
	for _, p := range frame {
		data = append(data, p.B, p.G, p.R)
	}
	if _, err := r.file.WriteAt(data, 0); err != nil {
		return err
	}
	if r.pan {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, r.file.Fd(), fbPan, uintptr(unsafe.Pointer(&r.info))); errno != 0 {
			return errno
		}
	}
	return nil
}
