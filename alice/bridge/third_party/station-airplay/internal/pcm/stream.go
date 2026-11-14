// Package pcm implements the small ALSA playback surface needed by AirPlay.
// It is used only by the isolated station-pcm process, never by the static receiver or its embedding host.
package pcm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"
)

// Config describes stereo, signed 16-bit little-endian PCM input.
type Config struct {
	Device string
	Rate   int
}

type device interface {
	write([]byte) int
	wait() int
	recover(int) int
	drain() int
	drop()
	err(int) error
}

// transfer keeps all ALSA calls on its caller's goroutine. Stop drops buffered
// audio; finite EOF drains it. Input must be closed by the caller on cancellation
// if its Read can block (the command does this for stdin).
func transfer(ctx context.Context, input io.Reader, d device) (result error) {
	defer func() {
		if result != nil {
			d.drop()
		}
	}()
	buffer := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := io.ReadFull(input, buffer)
		if err := ctx.Err(); err != nil {
			return err
		}
		if n%4 != 0 {
			return errors.New("PCM input ends with an incomplete stereo S16_LE frame")
		}
		for data := buffer[:n]; len(data) > 0; {
			if err := ctx.Err(); err != nil {
				return err
			}
			written := d.write(data)
			switch {
			case written > len(data)/4:
				return errors.New("ALSA returned an invalid frame count")
			case written > 0:
				data = data[written*4:]
			case written == 0 || written == -int(syscall.EAGAIN):
				if err := wait(ctx, d); err != nil {
					return err
				}
			case written == -int(syscall.EINTR):
				continue
			default:
				if err := ctx.Err(); err != nil {
					return err
				}
				if code := d.recover(written); code < 0 {
					return fmt.Errorf("write PCM: %w", d.err(code))
				}
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return drain(ctx, d)
		}
		if readErr != nil {
			return fmt.Errorf("read PCM: %w", readErr)
		}
	}
}
func wait(ctx context.Context, d device) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code := d.wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if code < 0 && code != -int(syscall.EINTR) {
		if code = d.recover(code); code < 0 {
			return fmt.Errorf("wait for PCM: %w", d.err(code))
		}
	}
	return nil
}
func drain(ctx context.Context, d device) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		code := d.drain()
		if code == 0 {
			return nil
		}
		if code != -int(syscall.EAGAIN) && code != -int(syscall.EINTR) {
			return fmt.Errorf("drain PCM: %w", d.err(code))
		}
		// Writable space can remain ready throughout drain. Poll completion at a
		// bounded rate instead of spinning or recovering/restarting the stream.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
