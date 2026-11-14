package event

import (
	"crypto/cipher"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// Event channels reverse control-channel key direction: the receiver sends with
// Events-Write and the sender replies with Events-Read. Each direction has its
// own monotonically increasing little-endian nonce counter. Key labels and
// server channel 4 direction are specified in the primary implementation:
// https://github.com/mikebrady/shairport-sync/blob/master/pair_ap/pair_homekit.c
type eventCipher struct {
	send, receive           cipher.AEAD
	sendCount, receiveCount uint64
}

func newEventCipher(secret []byte) (*eventCipher, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("invalid event shared secret")
	}
	derive := func(label string) (cipher.AEAD, error) {
		var key [32]byte
		defer clear(key[:])
		if _, err := io.ReadFull(hkdf.New(sha512.New, secret, []byte("Events-Salt"), []byte(label)), key[:]); err != nil {
			return nil, err
		}
		return chacha20poly1305.New(key[:])
	}
	send, err := derive("Events-Write-Encryption-Key")
	if err != nil {
		return nil, err
	}
	receive, err := derive("Events-Read-Encryption-Key")
	if err != nil {
		return nil, err
	}
	return &eventCipher{send: send, receive: receive}, nil
}

type encryptedConn struct {
	net.Conn
	crypt *eventCipher
	plain []byte
}

func (c *encryptedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.plain) == 0 {
		var h [2]byte
		if _, err := io.ReadFull(c.Conn, h[:]); err != nil {
			return 0, err
		}
		n := int(binary.LittleEndian.Uint16(h[:]))
		if n == 0 || n > 1024 {
			return 0, fmt.Errorf("invalid event record length")
		}
		data := make([]byte, n+16)
		if _, err := io.ReadFull(c.Conn, data); err != nil {
			return 0, err
		}
		if c.crypt.receiveCount == ^uint64(0) {
			return 0, fmt.Errorf("event receive nonce exhausted")
		}
		var nonce [12]byte
		binary.LittleEndian.PutUint64(nonce[4:], c.crypt.receiveCount)
		plain, err := c.crypt.receive.Open(nil, nonce[:], data, h[:])
		if err != nil {
			return 0, err
		}
		c.crypt.receiveCount++
		c.plain = plain
	}
	n := copy(p, c.plain)
	c.plain = c.plain[n:]
	return n, nil
}
func (c *encryptedConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), 1024)
		var h [2]byte
		binary.LittleEndian.PutUint16(h[:], uint16(n))
		var nonce [12]byte
		if c.crypt.sendCount == ^uint64(0) {
			return written, fmt.Errorf("event send nonce exhausted")
		}
		binary.LittleEndian.PutUint64(nonce[4:], c.crypt.sendCount)
		c.crypt.sendCount++
		frame := append(h[:], c.crypt.send.Seal(nil, nonce[:], p[:n], h[:])...)
		for len(frame) > 0 {
			count, err := c.Conn.Write(frame)
			if err != nil {
				return written, err
			}
			if count == 0 {
				return written, io.ErrShortWrite
			}
			frame = frame[count:]
		}
		p = p[n:]
		written += n
	}
	return written, nil
}
