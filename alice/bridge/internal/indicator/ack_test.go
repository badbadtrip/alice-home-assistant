package indicator

import (
	"testing"
	"time"
)

func TestAckShowsBriefly(t *testing.T) {
	c := &Controller{}
	now := time.Now()
	if _, ok := c.ack(now, 12); ok {
		t.Fatal("ack without a command")
	}
	c.Ack("next")
	if frame, ok := c.ack(time.Now().Add(AckDuration/2), 12); !ok || len(frame) != 12 {
		t.Fatalf("ack missing: %v %d", ok, len(frame))
	}
	if _, ok := c.ack(time.Now().Add(AckDuration+time.Millisecond), 12); ok {
		t.Fatal("ack outlived its time")
	}
}
