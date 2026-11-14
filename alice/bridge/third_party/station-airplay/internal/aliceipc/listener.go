// Package aliceipc is an experimental local Alice listener. Cancellation is
// best effort: IPC notifications provide no gate before native execution.
package aliceipc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type Config struct {
	Address      string
	CancelMethod int
	Logger       *log.Logger
}

type tracker struct {
	state                     int
	armed, handled            bool
	sawFinalMetadata          bool
	phrase, requestID         string
	lastPhrase, lastRequestID string
	lastHandled               time.Time
}

func (s *tracker) accept(e event, now time.Time) (string, string) {
	if e.statePresent && e.state == 1 && (s.state != 1 || e.requestID != "" && s.requestID != "" && e.requestID != s.requestID) {
		s.armed, s.handled, s.phrase, s.requestID = true, false, "", ""
		s.sawFinalMetadata = false
	}
	// Do not combine a delayed final from another request with the current
	// request's partial phrase. A new LISTENING cycle above owns any ID change.
	if e.requestID != "" && s.requestID != "" && e.requestID != s.requestID {
		return "", ""
	}
	// AliceState notifications can be sparse updates. Treat an absent state as
	// unchanged, rather than mistaking it for a transition to proto's default IDLE.
	if e.statePresent {
		s.state = e.state
	}
	s.sawFinalMetadata = s.sawFinalMetadata || e.finalPresent
	if e.requestID != "" {
		s.requestID = e.requestID
	}
	if e.phrase != "" {
		s.phrase = e.phrase
	}
	ready := e.final || !s.sawFinalMetadata && s.state != 1 && e.phrase != ""
	if !s.armed || s.handled || !ready || s.phrase == "" {
		return "", ""
	}
	s.handled = true
	if s.phrase == s.lastPhrase && s.requestID == s.lastRequestID && now.Sub(s.lastHandled) < 2*time.Second {
		return "", ""
	}
	s.lastPhrase, s.lastRequestID, s.lastHandled = s.phrase, s.requestID, now
	return command(s.phrase)
}

type job struct {
	action string
	at     time.Time
}

// Run reconnects until ctx ends. connected must remain true during a sender's
// pause. The callback must honor context cancellation; no utterances are logged.
func Run(ctx context.Context, cfg Config, connected func() bool, control func(context.Context, string) error) error {
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:9875"
	}
	if cfg.CancelMethod == 0 {
		cfg.CancelMethod = 1
	}
	if cfg.CancelMethod != 1 && cfg.CancelMethod != 6 {
		return errors.New("Alice cancel method must be 1 or 6")
	}
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("Alice IPC address must use a loopback IP")
	}
	if connected == nil || control == nil {
		return errors.New("Alice IPC requires sender callbacks")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	jobs := make(chan job, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-jobs:
				if ctx.Err() != nil || !connected() || time.Since(j.at) > time.Second {
					logger.Print("alice_ipc_media_skipped reason=stale_or_disconnected")
					continue
				}
				callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := control(callCtx, j.action)
				cancel()
				if err != nil {
					logger.Printf("alice_ipc_media_failed action=%s error_type=%T", j.action, err)
				} else {
					logger.Printf("alice_ipc_media_sent action=%s", j.action)
				}
			}
		}
	})
	defer wg.Wait()
	for ctx.Err() == nil {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", cfg.Address)
		if err == nil {
			logger.Printf("alice_ipc_connected cancel_method=%d", cfg.CancelMethod)
			closeOnCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
			err = listen(ctx, conn, cfg.CancelMethod, logger, connected, jobs)
			closeOnCancel()
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			break
		}
		logger.Printf("alice_ipc_reconnect error_type=%T", err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func listen(ctx context.Context, conn net.Conn, method int, logger *log.Logger, connected func() bool, jobs chan<- job) error {
	state := tracker{state: -1}
	for {
		frame, err := readFrame(conn)
		if err != nil {
			return err
		}
		e, ok, err := decodeEvent(frame)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		hash := sha256.Sum256([]byte(e.requestID))
		logger.Printf("alice_ipc_state state=%d state_present=%t final=%t final_present=%t phrase_present=%t request=%x", e.state, e.statePresent, e.final, e.finalPresent, e.phrase != "", hash[:4])
		requested, action := state.accept(e, time.Now())
		if action == "" || ctx.Err() != nil || !connected() {
			continue
		}
		// Only this reader produces jobs; the consumer can only make more room
		// while cancellation is written. Never cancel a command we cannot queue.
		if len(jobs) == cap(jobs) {
			logger.Print("alice_ipc_control_skipped reason=busy")
			continue
		}
		if err := conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return err
		}
		out := cancelFrame(method)
		if e.nativeEnvelope {
			out = nativeCancelFrame(method)
		}
		for len(out) > 0 {
			n, err := conn.Write(out)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("Alice IPC cancel write made no progress")
			}
			out = out[n:]
		}
		logger.Printf("alice_ipc_cancel_sent method=%d requested=%s action=%s", method, requested, action)
		jobs <- job{action: action, at: time.Now()}
	}
}
