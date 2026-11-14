package localstate

import (
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// quickCommands are the firmware's quick phrases (heard without «Алиса») and the
// player action each one means. «Громче/тише» stay with the firmware.
var quickCommands = map[string]string{
	"хватит": "pause", "стоп": "pause", "пауза": "pause",
	"играй": "resume", "продолжай": "resume", "продолжи": "resume",
	"дальше": "next", "следующий": "next", "назад": "previous",
}

// QuickCommand reads a type 210 frame: 252 Remoting { 7 "CommandSpotterCapability",
// 15 method { 1 = 2, 3 phrase } }. Anything not in quickCommands is dropped.
func QuickCommand(frame []byte) (phrase, action string, ok bool) {
	kind, okKind := varintField(frame, 253)
	remoting := bytesField(frame, 252)
	if !okKind || kind != 210 || remoting == nil {
		return "", "", false
	}
	if string(bytesField(remoting, 7)) != "CommandSpotterCapability" {
		return "", "", false
	}
	method := bytesField(remoting, 15)
	if m, ok := varintField(method, 1); !ok || m != 2 {
		return "", "", false
	}
	raw := bytesField(method, 3)
	if len(raw) == 0 || len(raw) > 64 {
		return "", "", false
	}
	phrase = strings.ToLower(strings.TrimSpace(string(raw)))
	action, ok = quickCommands[phrase]
	return phrase, action, ok
}

func varintField(b []byte, field protowire.Number) (uint64, bool) {
	var out uint64
	found := false
	walk(b, func(number protowire.Number, wire protowire.Type, v []byte, n uint64) {
		if number == field && wire == protowire.VarintType {
			out, found = n, true
		}
	})
	return out, found
}

func bytesField(b []byte, field protowire.Number) []byte {
	var out []byte
	walk(b, func(number protowire.Number, wire protowire.Type, v []byte, n uint64) {
		if number == field && wire == protowire.BytesType {
			out = v
		}
	})
	return out
}

// walk visits every top-level field; a malformed message stops the walk.
func walk(b []byte, visit func(protowire.Number, protowire.Type, []byte, uint64)) {
	for len(b) > 0 {
		number, wire, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		switch wire {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return
			}
			visit(number, wire, nil, v)
			n = m
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return
			}
			visit(number, wire, v, 0)
			n = m
		default:
			if n = protowire.ConsumeFieldValue(number, wire, b); n < 0 {
				return
			}
		}
		b = b[n:]
	}
}
