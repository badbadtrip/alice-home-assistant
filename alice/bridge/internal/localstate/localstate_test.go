package localstate

import (
	"bytes"
	"encoding/binary"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestFromRawFollowsMeasuredCurve(t *testing.T) {
	for raw, want := range map[int]float64{0: 0, 60: 1, 82: 2, 147: 5, 158: 5.5, 233: 9, 255: 10} {
		if got := FromRaw(raw).Level; got != want {
			t.Errorf("raw %d: got %v, want %v", raw, got, want)
		}
	}
	if FromRaw(158).Percent != 55 {
		t.Fatal("half levels show as 5 %")
	}
}

func TestReadAliceKeepsOnlyStateChanges(t *testing.T) {
	var stream bytes.Buffer
	for _, f := range []struct{ kind, state uint64 }{{3, 1}, {3, 1}, {210, 2}, {3, 3}, {3, 0}} {
		var payload []byte
		payload = protowire.AppendTag(payload, 1, protowire.VarintType)
		payload = protowire.AppendVarint(payload, f.state)
		var message []byte
		message = protowire.AppendTag(message, 252, protowire.BytesType)
		message = protowire.AppendBytes(message, payload)
		message = protowire.AppendTag(message, 253, protowire.VarintType)
		message = protowire.AppendVarint(message, f.kind)
		stream.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(message))))
		stream.Write(message)
	}
	var seen []string
	readAlice(&stream, func(state string) { seen = append(seen, state) })
	if want := []string{"listening", "thinking", "idle"}; len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("got %v", seen)
	}
}
