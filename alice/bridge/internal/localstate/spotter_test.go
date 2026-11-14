package localstate

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func spotterFrame(kind uint64, capability string, method uint64, phrase string) []byte {
	var m []byte
	m = protowire.AppendTag(m, 1, protowire.VarintType)
	m = protowire.AppendVarint(m, method)
	m = protowire.AppendTag(m, 3, protowire.BytesType)
	m = protowire.AppendString(m, phrase)
	var r []byte
	r = protowire.AppendTag(r, 7, protowire.BytesType)
	r = protowire.AppendString(r, capability)
	r = protowire.AppendTag(r, 15, protowire.BytesType)
	r = protowire.AppendBytes(r, m)
	var f []byte
	f = protowire.AppendTag(f, 252, protowire.BytesType)
	f = protowire.AppendBytes(f, r)
	f = protowire.AppendTag(f, 253, protowire.VarintType)
	f = protowire.AppendVarint(f, kind)
	return f
}

func TestQuickCommand(t *testing.T) {
	cases := []struct {
		frame  []byte
		action string
	}{
		{spotterFrame(210, "CommandSpotterCapability", 2, "Стоп"), "pause"},
		{spotterFrame(210, "CommandSpotterCapability", 2, "дальше"), "next"},
		{spotterFrame(210, "CommandSpotterCapability", 2, "громче"), ""},
		{spotterFrame(210, "CommandSpotterCapability", 1, "стоп"), ""},
		{spotterFrame(210, "Other", 2, "стоп"), ""},
		{spotterFrame(3, "CommandSpotterCapability", 2, "стоп"), ""},
	}
	for i, c := range cases {
		_, action, _ := QuickCommand(c.frame)
		if action != c.action {
			t.Errorf("case %d: action %q, want %q", i, action, c.action)
		}
	}
	full := spotterFrame(210, "CommandSpotterCapability", 2, "пауза")
	for i := range full {
		QuickCommand(full[:i]) // must not panic on truncated frames
	}
}
