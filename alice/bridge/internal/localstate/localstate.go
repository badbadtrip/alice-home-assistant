// Package localstate reads what the speaker itself knows, without the cloud: the live
// volume from the ALSA mixer and Alice's listening state from the firmware's local socket.
package localstate

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Volume is the speaker loudness on Alice's 0-10 scale; buttons move it by halves.
type Volume struct {
	Level   float64 `json:"level"`
	Percent int     `json:"percent"`
}

// FromRaw turns the Master mixer value (0-255) into Alice's level. Measured on Midi and
// Lite 2: level 0 is 0, levels 1-10 run evenly from 60 to 255.
func FromRaw(raw int) Volume {
	if raw <= 0 {
		return Volume{}
	}
	level := 1 + float64(raw-60)/(195.0/9)
	level = math.Max(0, math.Min(10, math.Round(level*2)/2))
	return Volume{Level: level, Percent: int(math.Round(level * 10))}
}

var playback = regexp.MustCompile(`Playback (\d+) \[`)

// ReadVolume asks the mixer once; it runs only when the mixer reported a change.
func ReadVolume(ctx context.Context) (Volume, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "amixer", "-c0", "get", "Master").Output()
	if err != nil {
		return Volume{}, err
	}
	match := playback.FindSubmatch(out)
	if match == nil {
		return Volume{}, errors.New("no Master playback value")
	}
	raw, _ := strconv.Atoi(string(match[1]))
	return FromRaw(raw), nil
}

// WatchVolume reports the volume at start and after every mixer change. Where the mixer
// cannot be watched it checks every two seconds instead.
func WatchVolume(ctx context.Context, report func(Volume)) {
	var last *Volume
	check := func() {
		if volume, err := ReadVolume(ctx); err == nil && (last == nil || *last != volume) {
			last = &volume
			report(volume)
		}
	}
	check()
	events := mixerEvents(ctx)
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-events:
			if !open {
				events = nil
				continue
			}
			// A button press moves several controls at once; one read covers them all.
			time.Sleep(60 * time.Millisecond)
			drain(events)
			check()
		case <-poll.C:
			if events == nil {
				check()
			}
		}
	}
}

func drain(events <-chan struct{}) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

// Alice states as the firmware numbers them.
var aliceStates = map[uint64]string{0: "idle", 1: "listening", 2: "speaking", 3: "thinking"}

// AliceSocket is the firmware's local event socket.
const AliceSocket = "127.0.0.1:9875"

// WatchAlice reports Alice's state on every change. The socket also carries account
// tokens and recognised speech: only the state number is ever kept.
func WatchAlice(ctx context.Context, address string, report func(string)) {
	for ctx.Err() == nil {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			go func() { <-ctx.Done(); conn.Close() }()
			readAlice(bufio.NewReader(conn), report)
			conn.Close()
			report("idle")
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func readAlice(r io.Reader, report func(string)) {
	last := ""
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(header)
		if size > 1<<20 {
			return
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(r, frame); err != nil {
			return
		}
		if state, ok := aliceState(frame); ok && state != last {
			last = state
			report(state)
		}
	}
}

// aliceState reads field 253 (message type) and, for type 3, field 1 of payload 252.
func aliceState(frame []byte) (string, bool) {
	var kind uint64
	var payload []byte
	for len(frame) > 0 {
		number, wire, n := protowire.ConsumeTag(frame)
		if n < 0 {
			return "", false
		}
		frame = frame[n:]
		switch {
		case number == 253 && wire == protowire.VarintType:
			v, m := protowire.ConsumeVarint(frame)
			if m < 0 {
				return "", false
			}
			kind, n = v, m
		case number == 252 && wire == protowire.BytesType:
			v, m := protowire.ConsumeBytes(frame)
			if m < 0 {
				return "", false
			}
			payload, n = v, m
		default:
			n = protowire.ConsumeFieldValue(number, wire, frame)
			if n < 0 {
				return "", false
			}
		}
		frame = frame[n:]
	}
	if kind != 3 {
		return "", false
	}
	state := uint64(0)
	for len(payload) > 0 {
		number, wire, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return "", false
		}
		payload = payload[n:]
		if number == 1 && wire == protowire.VarintType {
			v, m := protowire.ConsumeVarint(payload)
			if m < 0 {
				return "", false
			}
			state, n = v, m
		} else if n = protowire.ConsumeFieldValue(number, wire, payload); n < 0 {
			return "", false
		}
		payload = payload[n:]
	}
	name, ok := aliceStates[state]
	return name, ok
}
