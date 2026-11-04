package indicator

import (
	"context"
	"testing"
	"time"
)

func active(c *Controller) int { c.mu.Lock(); defer c.mu.Unlock(); return c.active }

func TestLocalTurnDoesNotAcquireRing(t *testing.T) {
	c := &Controller{}
	_, finish := WithTurn(t.Context())
	finish()
	if active(c) != 0 {
		t.Fatal("local turn claimed LEDs")
	}
}

func TestModelToolModelKeepsOneOwnershipUntilTurnEnds(t *testing.T) {
	c := &Controller{}
	ctx, finish := WithTurn(t.Context())
	c.Request(ctx)()
	if active(c) != 1 {
		t.Fatal("first model request did not claim ring")
	}
	// Returning from HTTP and waiting for tools must not restore the firmware.
	c.Request(ctx)()
	nested, nestedFinish := WithTurn(ctx)
	c.Request(nested)()
	nestedFinish()
	if active(c) != 1 {
		t.Fatal("model steps reacquired or released LEDs")
	}
	finish()
	finish()
	if active(c) != 0 {
		t.Fatal("turn completion did not release LEDs")
	}
	c.Request(ctx)()
	if active(c) != 0 {
		t.Fatal("late completion reclaimed LEDs")
	}
}

func TestCancelReleasesOnlyItsOwnTurn(t *testing.T) {
	c := &Controller{}
	parent, cancel := context.WithCancel(t.Context())
	first, finishFirst := WithTurn(parent)
	defer finishFirst()
	second, finishSecond := WithTurn(t.Context())
	defer finishSecond()
	c.Request(first)()
	c.Request(second)()
	cancel()
	until := time.Now().Add(time.Second)
	for active(c) != 1 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if active(c) != 1 {
		t.Fatal("cancel did not retain the overlapping turn")
	}
	finishSecond()
	if active(c) != 0 {
		t.Fatal("last turn did not release ring")
	}
}
