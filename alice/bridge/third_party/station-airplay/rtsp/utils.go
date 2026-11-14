// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package rtsp

import (
	"bufio"
	"fmt"
)

const (
	rtspMaxContentLength = 128 * 1024
)

func readByteEqual(rb *bufio.Reader, cmp byte) error {
	byt, err := rb.ReadByte()
	if err != nil {
		return err
	}

	if byt != cmp {
		return fmt.Errorf("expected '%c', got '%c'", cmp, byt)
	}

	return nil
}

func readBytesLimited(rb *bufio.Reader, delim byte, n int) ([]byte, error) {
	for i := 1; i <= n; i++ {
		byts, err := rb.Peek(i)
		if err != nil {
			return nil, err
		}

		if byts[len(byts)-1] == delim {
			_, err = rb.Discard(len(byts))
			if err != nil {
				return nil, err
			}
			return byts, nil
		}
	}
	return nil, fmt.Errorf("buffer length exceeds %d", n)
}
