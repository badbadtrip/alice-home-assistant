package bridge

import (
 "context"
 "testing"
 "time"
 "github.com/coder/websocket"
)

func TestFastHomeSkipsThinkingCue(t *testing.T) {
 n := NewNative(t.Context(), func(context.Context, websocket.MessageType, []byte) error {
  t.Error("fast home command requested speech before result")
  return nil
 }, func(context.Context, websocket.MessageType, []byte) error {
  t.Error("fast home command emitted progress")
  return nil
 }, nil)
 defer n.Close()
 n.VoiceCues = &VoiceCues{}
 ctx, cancel := context.WithCancel(t.Context())
 defer cancel()
 p := &nativeRequest{ctx:ctx, cancel:cancel}
 started := time.Now()
 stop := n.progressAfter(p, time.Second)
 if time.Since(started) > 500*time.Millisecond { t.Fatal("progress blocked command execution") }
 stop()
}
