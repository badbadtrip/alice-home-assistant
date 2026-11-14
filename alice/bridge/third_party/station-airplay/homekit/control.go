package homekit

import (
	"bytes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
	"io"
)

type ControlSession struct {
	send, receive           cipher.AEAD
	sendCount, receiveCount uint64
}

func NewControlSession(key []byte, client bool) (*ControlSession, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("invalid control shared secret")
	}
	derive := func(label string) (cipher.AEAD, error) {
		b := make([]byte, 32)
		if _, err := io.ReadFull(hkdf.New(sha512.New, key, []byte("Control-Salt"), []byte(label)), b); err != nil {
			return nil, err
		}
		return chacha20poly1305.New(b)
	}
	read, err := derive("Control-Read-Encryption-Key")
	if err != nil {
		return nil, err
	}
	write, err := derive("Control-Write-Encryption-Key")
	if err != nil {
		return nil, err
	}
	if client {
		return &ControlSession{send: write, receive: read}, nil
	}
	return &ControlSession{send: read, receive: write}, nil
}
func (s *ControlSession) Encrypt(r io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(io.LimitReader(r, 256*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 256*1024 {
		return nil, fmt.Errorf("control message too large")
	}
	var out bytes.Buffer
	for len(data) > 0 {
		n := min(1024, len(data))
		var header [2]byte
		binary.LittleEndian.PutUint16(header[:], uint16(n))
		var nonce [12]byte
		binary.LittleEndian.PutUint64(nonce[4:], s.sendCount)
		s.sendCount++
		out.Write(header[:])
		out.Write(s.send.Seal(nil, nonce[:], data[:n], header[:]))
		data = data[n:]
	}
	return &out, nil
}
func (s *ControlSession) Decrypt(r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	for {
		var header [2]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF {
				return &out, nil
			}
			return nil, err
		}
		n := int(binary.LittleEndian.Uint16(header[:]))
		if n == 0 || n > 1024 || out.Len()+n > 256*1024 {
			return nil, fmt.Errorf("invalid control record length")
		}
		encrypted := make([]byte, n+16)
		if _, err := io.ReadFull(r, encrypted); err != nil {
			return nil, err
		}
		var nonce [12]byte
		binary.LittleEndian.PutUint64(nonce[4:], s.receiveCount)
		plain, err := s.receive.Open(nil, nonce[:], encrypted, header[:])
		if err != nil {
			return nil, err
		}
		s.receiveCount++
		out.Write(plain)
	}
}
