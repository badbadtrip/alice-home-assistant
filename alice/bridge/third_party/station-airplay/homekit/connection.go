package homekit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/brutella/hc/hap"
	"io"
	"net"
	"sync"
)

// Connection reads exactly one authenticated HAP record at a time. The upstream
// hap.Connection creates temporary buffered readers which can discard the next record.
type Connection struct {
	net.Conn
	ctx     hap.Context
	plain   []byte
	writeMu sync.Mutex
}

func NewConnection(raw net.Conn, ctx hap.Context) *Connection {
	c := &Connection{Conn: raw, ctx: ctx}
	ctx.SetSessionForConnection(hap.NewSession(c), c)
	return c
}
func (c *Connection) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	session := c.ctx.GetSessionForConnection(c)
	if session == nil {
		return 0, io.EOF
	}
	dec := session.Decrypter()
	if dec == nil {
		// Do not prefetch encrypted data into the RTSP plaintext reader before the
		// pair-verify response has been written and encryption has been activated.
		return c.Conn.Read(b[:1])
	}
	if len(c.plain) == 0 {
		var header [2]byte
		if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
			return 0, err
		}
		n := int(binary.LittleEndian.Uint16(header[:]))
		if n == 0 || n > 1024 {
			return 0, fmt.Errorf("invalid HAP record length %d", n)
		}
		frame := make([]byte, 2+n+16)
		copy(frame, header[:])
		if _, err := io.ReadFull(c.Conn, frame[2:]); err != nil {
			return 0, err
		}
		r, err := dec.Decrypt(bytes.NewReader(frame))
		if err != nil {
			return 0, err
		}
		c.plain, err = io.ReadAll(io.LimitReader(r, 1025))
		if err != nil {
			return 0, err
		}
		if len(c.plain) != n {
			return 0, fmt.Errorf("invalid HAP plaintext length")
		}
	}
	n := copy(b, c.plain)
	c.plain = c.plain[n:]
	return n, nil
}
func (c *Connection) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	session := c.ctx.GetSessionForConnection(c)
	if session == nil {
		return 0, io.ErrClosedPipe
	}
	wire := b
	if enc := session.Encrypter(); enc != nil {
		r, err := enc.Encrypt(bytes.NewReader(b))
		if err != nil {
			return 0, err
		}
		wire, err = io.ReadAll(r)
		if err != nil {
			return 0, err
		}
	}
	for len(wire) > 0 {
		n, err := c.Conn.Write(wire)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.ErrShortWrite
		}
		wire = wire[n:]
	}
	return len(b), nil
}
func (c *Connection) Close() error { c.ctx.DeleteSessionForConnection(c); return c.Conn.Close() }
