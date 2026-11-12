package sendspin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeOutput struct {
	mu     sync.Mutex
	data   []byte
	closed bool
}

func (f *fakeOutput) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append(f.data, p...)
	return len(p), nil
}
func (f *fakeOutput) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

type opener struct {
	mu    sync.Mutex
	opens []*fakeOutput
	rates []int
}

func (o *opener) open(_ context.Context, rate, _ int) (Output, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &fakeOutput{}
	o.opens = append(o.opens, out)
	o.rates = append(o.rates, rate)
	return out, nil
}

func formatFrame(rate uint32) []byte {
	b := []byte{'F', 0, 0, 0, 0, 2}
	binary.BigEndian.PutUint32(b[1:], rate)
	return b
}

func audioFrame(samples ...int16) []byte {
	pcm := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(s))
	}
	b := []byte{'A'}
	b = binary.BigEndian.AppendUint32(b, uint32(len(pcm)))
	return append(b, pcm...)
}

func serve(t *testing.T, frames ...[]byte) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Alice-Speaker") != "spk" || r.URL.Path != "/sendspin/stream" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for _, f := range frames {
			_, _ = w.Write(f)
		}
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	return server, release
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPlaysAtFullVolumeAndStopsOnEnd(t *testing.T) {
	o := &opener{}
	server, release := serve(t, []byte{'V', 100, 0}, formatFrame(48000), audioFrame(1000, -1000, 32767, -32768), []byte{'E'})
	defer close(release)
	p := &Player{URL: server.URL, Token: "tok", Speaker: "spk", Open: o.open}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		if len(o.opens) != 1 {
			return false
		}
		o.opens[0].mu.Lock()
		defer o.opens[0].mu.Unlock()
		return o.opens[0].closed
	})
	got := o.opens[0].data
	want := audioFrame(1000, -1000, 32767, -32768)[5:]
	if !bytes.Equal(got, want) {
		t.Fatalf("pcm changed at full volume: %v != %v", got, want)
	}
	if o.rates[0] != 48000 {
		t.Fatalf("rate %d", o.rates[0])
	}
	if p.Streaming() {
		t.Fatal("still streaming after end")
	}
}

func TestClearReopensAndFormatChangeRestarts(t *testing.T) {
	o := &opener{}
	server, release := serve(t, formatFrame(48000), audioFrame(1, 1), []byte{'C'}, audioFrame(2, 2), formatFrame(44100), audioFrame(3, 3))
	defer close(release)
	p := &Player{URL: server.URL, Token: "tok", Speaker: "spk", Open: o.open}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return len(o.opens) == 3 })
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.opens[0].closed || !o.opens[1].closed || o.opens[2].closed {
		t.Fatal("clear and format change must close the previous output only")
	}
	if o.rates[2] != 44100 {
		t.Fatalf("new format not used: %v", o.rates)
	}
	if !p.Streaming() {
		t.Fatal("format frame should mark streaming")
	}
}

func TestMuteSilencesAndRejectsBadFrames(t *testing.T) {
	o := &opener{}
	server, release := serve(t, []byte{'V', 80, 1}, formatFrame(48000), audioFrame(12000, -12000), []byte{'?'})
	defer close(release)
	p := &Player{URL: server.URL, Token: "tok", Speaker: "spk", Open: o.open}
	p.volume.Store(100)
	s := &stream{player: p, ctx: context.Background()}
	resp, err := http.DefaultClient.Do(func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, server.URL+"/sendspin/stream", nil)
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("X-Alice-Speaker", "spk")
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	err = s.read(bufioReader(resp.Body), func() {})
	if err == nil {
		t.Fatal("unknown frame must end the session")
	}
	if got := o.opens[0].data; !bytes.Equal(got, make([]byte, 4)) {
		t.Fatalf("muted audio must be silent: %v", got)
	}
}

func TestGainRampsWithoutJumps(t *testing.T) {
	pcm := bytes.Repeat(binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, 20000), 20000), 4800)
	end := applyGain(pcm, 1, duckGain)
	if end != duckGain {
		t.Fatalf("ramp did not reach target over 100 ms: %v", end)
	}
	prev := int16(20000)
	for i := 0; i < len(pcm); i += 4 {
		v := int16(binary.LittleEndian.Uint16(pcm[i:]))
		if prev-v > 20 {
			t.Fatalf("step %d at frame %d is a click", prev-v, i/4)
		}
		prev = v
	}
}

func TestDuckFollowsAliceState(t *testing.T) {
	p := &Player{}
	p.volume.Store(100)
	s := &stream{player: p}
	p.SetAliceState("speaking")
	if s.target() != duckGain {
		t.Fatal("music must duck while Alice speaks")
	}
	p.SetAliceState("idle")
	if s.target() != 1 {
		t.Fatal("music must return after Alice")
	}
}

func TestControlPostsAction(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + r.Header.Get("X-Alice-Speaker") + " " + string(b)
	}))
	defer server.Close()
	p := &Player{URL: server.URL + "/", Token: "tok", Speaker: "spk"}
	if err := p.Control(context.Background(), "pause"); err != nil {
		t.Fatal(err)
	}
	if got != `POST /sendspin/control spk {"action":"pause"}` {
		t.Fatalf("got %q", got)
	}
}

func TestRejectedStreamRetries(t *testing.T) {
	calls := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- struct{}{}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p := &Player{URL: server.URL, Token: "tok", Speaker: "spk", Open: (&opener{}).open}
	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	<-calls
	<-calls // the second attempt after a one second pause
	cancel()
	if p.Linked() {
		t.Fatal("a refused request is not a link")
	}
}

func bufioReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
