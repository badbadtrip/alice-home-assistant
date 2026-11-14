package audio

import (
	"context"
	"io"
	"log"
	"testing"
	"time"
)

// Measured on Midi, 2026-11-14: on pause the iPhone sends an immediate
// FLUSHBUFFERED, starts a new RTP timeline at a random point and prebuffers
// into it; the resume anchor is in the new timeline, 0.3 s before its first frame.

func testServer(t *testing.T) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewServer(&Player{Context: ctx, Logger: log.New(io.Discard, "", 0)})
}

func frame(seq, ts uint32) *PCMFrame {
	return &PCMFrame{TcpPacket: TcpPacket{SequenceNumber: seq, Timestamp: ts}}
}

const aacFrame = 1024

func TestPrebufferAfterImmediateFlushSurvivesTimelineBelowOldAnchor(t *testing.T) {
	s := testServer(t)
	s.SetRateAnchorTime(500_000_000, time.Now()) // old timeline, above the new one
	s.SetRate0()
	s.Flush(0, 15905948)
	s.ForgetAnchor()
	// The new timeline starts "below" the old anchor: before the patch every
	// prebuffered frame was dropped as late, leaving an 8.6 s hole on resume.
	for i := uint32(0); i < 20; i++ {
		if err := s.enqueue(frame(15905949+i, 436_606_130+i*aacFrame)); err != nil {
			t.Fatal(err)
		}
	}
	if s.buffer.Len() != 20 {
		t.Fatalf("prebuffer kept %d of 20 frames", s.buffer.Len())
	}
	resume := uint32(436_606_130 - 13_230) // 0.3 s before the first frame
	s.SetRateAnchorTime(resume, time.Now())
	if s.buffer.Len() != 20 || s.anchorRTP != resume {
		t.Fatalf("resume kept %d frames, anchor %d", s.buffer.Len(), s.anchorRTP)
	}
}

func TestAnchorDropsFramesOfAnotherTimelineAndFollowsTheAudio(t *testing.T) {
	s := testServer(t)
	for i := uint32(0); i < 10; i++ {
		if err := s.enqueue(frame(100+i, 441_410_555+i*aacFrame)); err != nil {
			t.Fatal(err)
		}
	}
	// Seen at 16:17:18: the anchor was in another timeline than the audio and
	// the receiver "waited" 10 h 44 min (int32 wrap) until the next command.
	s.SetRateAnchorTime(3_031_902_088, time.Now())
	if s.buffer.Len() != 0 {
		t.Fatalf("frames outside the anchor window stayed: %d", s.buffer.Len())
	}
	next := uint32(441_410_555 + 10*aacFrame)
	if err := s.enqueue(frame(110, next)); err != nil {
		t.Fatal(err)
	}
	if s.buffer.Len() != 1 || s.anchorRTP != next-uint32(44100*anchorLeadMS/1000) {
		t.Fatalf("audio not followed: frames %d anchor %d", s.buffer.Len(), s.anchorRTP)
	}
}

func TestNormalResumeKeepsAnchorAndBuffer(t *testing.T) {
	s := testServer(t)
	anchor := uint32(2_182_382_928)
	for i := uint32(0); i < 128; i++ {
		if err := s.enqueue(frame(15910607+i, anchor+13_222+i*aacFrame)); err != nil {
			t.Fatal(err)
		}
	}
	s.SetRateAnchorTime(anchor, time.Now())
	if s.buffer.Len() != 128 || s.anchorRTP != anchor || s.awaitFirst {
		t.Fatalf("frames %d anchor %d await %v", s.buffer.Len(), s.anchorRTP, s.awaitFirst)
	}
	// Frames that really are late for the anchor are still dropped.
	s.mu.Lock()
	s.buffer.filter(func(*PCMFrame) bool { return false })
	s.mu.Unlock()
	if err := s.enqueue(frame(1, anchor-aacFrame)); err != nil {
		t.Fatal(err)
	}
	if s.buffer.Len() != 0 {
		t.Fatal("a late frame was kept")
	}
}

func TestDeferredFlushKeepsAnchor(t *testing.T) {
	s := testServer(t)
	s.SetRateAnchorTime(1000, time.Now())
	s.Flush(500, 600)
	if !s.haveAnchor {
		t.Fatal("a ranged flush must not drop the anchor")
	}
}
