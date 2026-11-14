//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// A descriptor inherited across exec may be in blocking mode and absent from
// Go's poller. Setting nonblocking before NewFile lets Close interrupt Read even
// when the parent keeps the pipe's writer open.
func interruptibleInput(source *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(source.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate PCM input: %w", err)
	}
	syscall.CloseOnExec(fd)
	if err = syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("make PCM input interruptible: %w", err)
	}
	return os.NewFile(uintptr(fd), source.Name()), nil
}
