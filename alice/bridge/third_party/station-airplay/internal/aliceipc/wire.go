package aliceipc

import (
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"
)

const maxFrame = 1 << 20

var errWire = errors.New("invalid Alice IPC frame")

type field struct {
	wire byte
	n    uint64
	data []byte
}

// fields reads the small part of protobuf wire format used by local Alice IPC.
// Unknown fields are skipped, while malformed lengths and varints are rejected.
func fields(data []byte) (map[int]field, error) {
	out := make(map[int]field)
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 || tag>>3 > (1<<29)-1 {
			return nil, errWire
		}
		data = data[n:]
		f := field{wire: byte(tag & 7)}
		switch f.wire {
		case 0:
			f.n, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, errWire
			}
			data = data[n:]
		case 1, 5:
			n = 8
			if f.wire == 5 {
				n = 4
			}
			if len(data) < n {
				return nil, errWire
			}
			data = data[n:]
		case 2:
			size, used := binary.Uvarint(data)
			if used <= 0 || size > uint64(len(data)-used) {
				return nil, errWire
			}
			data = data[used:]
			f.data, data = data[:int(size)], data[int(size):]
		default:
			return nil, errWire
		}
		out[int(tag>>3)] = f
	}
	return out, nil
}

type event struct {
	nativeEnvelope      bool
	state               int
	statePresent        bool
	phrase, requestID   string
	final, finalPresent bool
}

func decodeEvent(data []byte) (event, bool, error) {
	outer, err := fields(data)
	if err != nil {
		return event{}, false, err
	}
	// Orion factory-13 sends native protobuf oneof fields (3 / 210),
	// rather than the generic payload/type envelope (252 / 253).
	native := false
	payload, kind := outer[252], outer[253].n
	if payload.wire != 2 {
		switch {
		case outer[3].wire == 2:
			payload, kind, native = outer[3], 3, true
		case outer[210].wire == 2:
			payload, kind, native = outer[210], 210, true
		default:
			return event{}, false, nil
		}
	}
	var state []byte
	switch kind {
	case 3:
		state = payload.data
	case 210:
		remote, err := fields(payload.data)
		if err != nil {
			return event{}, false, err
		}
		if string(remote[7].data) != "AliceCapability" || remote[18].wire != 2 {
			return event{}, false, nil
		}
		listener, err := fields(remote[18].data)
		if err != nil {
			return event{}, false, err
		}
		if listener[1].n != 0 || listener[2].wire != 2 {
			return event{}, false, nil
		}
		state = listener[2].data
	default:
		return event{}, false, nil
	}
	f, err := fields(state)
	if err != nil {
		return event{}, false, err
	}
	stateField, statePresent := f[1]
	if statePresent && stateField.wire != 0 {
		return event{}, false, errWire
	}
	for _, key := range []int{2, 7, 15} {
		if v, ok := f[key]; ok && v.wire != 2 {
			return event{}, false, errWire
		}
	}
	e := event{nativeEnvelope: native, state: int(stateField.n), statePresent: statePresent, phrase: string(f[2].data), requestID: string(f[7].data)}
	if req, ok := f[15]; ok && req.wire == 2 {
		r, err := fields(req.data)
		if err != nil {
			return event{}, false, err
		}
		if v, ok := r[2]; ok && v.wire != 0 {
			return event{}, false, errWire
		}
		e.final = r[2].n != 0
		e.finalPresent = true
		if len(r[1].data) > 0 {
			e.phrase = string(r[1].data)
		}
	}
	if len(e.phrase) > 1024 || len(e.requestID) > 256 || !utf8.ValidString(e.phrase) {
		return event{}, false, errWire
	}
	return e, true, nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrame {
		return nil, errWire
	}
	data := make([]byte, n)
	_, err := io.ReadFull(r, data)
	return data, err
}

func number(dst []byte, key, n uint64) []byte {
	dst = binary.AppendUvarint(dst, key<<3)
	return binary.AppendUvarint(dst, n)
}

func bytesField(dst []byte, key uint64, data []byte) []byte {
	dst = binary.AppendUvarint(dst, key<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(data)))
	return append(dst, data...)
}

func cancelFrame(method int) []byte {
	remote := bytesField(nil, 7, []byte("AliceCapability"))
	remote = bytesField(remote, 8, number(nil, 1, uint64(method)))
	outer := bytesField(nil, 252, remote)
	outer = number(outer, 253, 210)
	frame := binary.LittleEndian.AppendUint32(nil, uint32(len(outer)))
	return append(frame, outer...)
}

func nativeCancelFrame(method int) []byte {
	remote := bytesField(nil, 7, []byte("AliceCapability"))
	remote = bytesField(remote, 8, number(nil, 1, uint64(method)))
	outer := bytesField(nil, 210, remote)
	frame := binary.LittleEndian.AppendUint32(nil, uint32(len(outer)))
	return append(frame, outer...)
}
