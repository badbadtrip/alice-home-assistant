// Adapted from ibiscum/goplay2; modifications Copyright 2026 Station AirPlay contributors.
package rtsp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

type body []byte

func (b *body) read(header Header, rb *bufio.Reader) error {
	cls, ok := header["Content-Length"]
	if !ok {
		*b = nil
		return nil
	}

	if len(cls) != 1 {
		return fmt.Errorf("ambiguous Content-Length")
	}

	cl, err := strconv.ParseInt(cls[0], 10, 64)
	if err != nil || cl < 0 {
		return fmt.Errorf("invalid Content-Length")
	}

	if cl > rtspMaxContentLength {
		return fmt.Errorf("Content-Length exceeds %d (it's %d)",
			rtspMaxContentLength, cl)
	}

	*b = make([]byte, cl)
	n, err := io.ReadFull(rb, *b)
	if err != nil && n != len(*b) {
		return err
	}

	return nil
}

func (b body) write(bw *bufio.Writer) error {
	if len(b) == 0 {
		return nil
	}

	_, err := bw.Write(b)
	if err != nil {
		return err
	}

	return nil
}
