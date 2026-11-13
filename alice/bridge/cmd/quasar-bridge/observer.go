package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"

	"quasarbridge/internal/wire"
)

// frameObserver counts the frames between the speaker and the cloud. With debug it also
// logs their kinds and sizes: never values, audio or authentication.
func frameObserver(clientCount, serverCount *atomic.Int64, debugFrames bool) func(string, websocket.MessageType, []byte) {
	return func(direction string, kind websocket.MessageType, data []byte) {
		d := wire.ClientToServer
		var count int64
		if direction == "server" {
			d = wire.ServerToClient
			count = serverCount.Add(1)
		} else {
			count = clientCount.Add(1)
		}
		if !debugFrames {
			return
		}
		frame := wire.Decode(data, d)
		name := frame.Kind()
		if (name != "opaque" && name != "AsrResult") || count <= 8 || count%100 == 0 {
			attrs := []any{"direction", direction, "transport_type", int(kind), "event", name, "bytes", len(data), "count", count}
			if len(frame.Prefix) == 4 {
				attrs = append(attrs, "prefix_tag", string(frame.Prefix))
			}
			if frame.Client != nil {
				e := frame.Client.GetEvent()
				if req := e.GetTextInput(); req != nil {
					attrs = append(attrs, "wait_client_approval", req.GetRequest().GetUniproxyOptions().GetWaitClientApproval())
				}
				if req := e.GetVoiceInput(); req != nil {
					attrs = append(attrs, "wait_client_approval", req.GetRequest().GetUniproxyOptions().GetWaitClientApproval())
				}
			}
			if name == "opaque" && count <= 8 {
				attrs = append(attrs, "framing", wire.Probe(data, d))
			}
			if name == "opaque" && kind == websocket.MessageText {
				// Keys only: never emit request values, audio or authentication.
				var obj map[string]jsontext.Value
				if json.Unmarshal(data, &obj) == nil {
					var keys []string
					for key := range obj {
						if len(key) < 40 && !strings.ContainsAny(key, "\n\r") {
							keys = append(keys, key)
						}
					}
					attrs = append(attrs, "json_keys", keys)
				}
			}
			slog.Info("frame", attrs...)
		}
	}
}
