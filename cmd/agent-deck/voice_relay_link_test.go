package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// failingRunner fails every connect at once (host unreachable).
type failingRunner struct {
	mu    sync.Mutex
	times []time.Time
}

func (f *failingRunner) RunStdinStreamIO(context.Context, io.Reader, io.Writer, io.Writer, ...string) error {
	f.mu.Lock()
	f.times = append(f.times, time.Now())
	f.mu.Unlock()
	return errors.New("connect refused")
}

// S1 (rework round 1): while the remote is unreachable the link retries
// at least every second, so a remote is right again about a second after
// the link returns (it was 6-10 s with backoff growing to 8 s).
func TestRelayLink_ReconnectsWithinASecond(t *testing.T) {
	f := &failingRunner{}
	l := newRelayLink("box", f, io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	l.run(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.times) < 4 {
		t.Fatalf("%d attempts in 4.5 s", len(f.times))
	}
	for i := 1; i < len(f.times); i++ {
		if gap := f.times[i].Sub(f.times[i-1]); gap > 1200*time.Millisecond {
			t.Fatalf("attempt %d came %s after the previous one; want at most ~1 s", i, gap)
		}
	}
}

// lineRunner records the mirror lines the receiver gets until stdin ends.
type lineRunner struct {
	mu    sync.Mutex
	lines []voicestate.Mirror
}

func (r *lineRunner) RunStdinStreamIO(ctx context.Context, in io.Reader, _ io.Writer, _ io.Writer, _ ...string) error {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var m voicestate.Mirror
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Schema != "" {
			r.mu.Lock()
			r.lines = append(r.lines, m)
			r.mu.Unlock()
		}
	}
	<-ctx.Done()
	return nil
}

// B2 (rework round 1): on shutdown the relay offers "off" and waits for each
// link to write it before closing the link.
func TestRelayLink_FlushWritesTheLastMirrorBeforeClose(t *testing.T) {
	r := &lineRunner{}
	l := newRelayLink("box", r, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.run(ctx); close(done) }()
	on := voicestate.MirrorFor(&voicestate.State{On: true, Phase: "listening"}, "box", "mac", time.Now().UnixMilli())
	l.offer(on)
	l.offer(voicestate.MirrorFor(nil, "box", "mac", time.Now().UnixMilli()))
	if !l.flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	cancel()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 || r.lines[len(r.lines)-1].On {
		t.Fatalf("the receiver's last mirror is not the off sent at shutdown: %d lines", len(r.lines))
	}
}
