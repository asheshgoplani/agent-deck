package tmux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// persistedOutputFrames counts tmux.output frames for session on disk.
func persistedOutputFrames(t *testing.T, dir, session string) (output, markers int) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var fr struct {
				Kind      string `json:"kind"`
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(sc.Bytes(), &fr) != nil || fr.SessionID != session {
				continue
			}
			switch fr.Kind {
			case events.KindTmuxOutput:
				output++
			case "test.marker":
				markers++
			}
		}
		fh.Close()
	}
	return output, markers
}

// TestPipeManager_OutputTapNeedsSubscriber (#2481 item 6): with no bus
// follower, N %output events reach the live onOutput consumer but write zero
// tmux.output frames; once a follower demands the kind, frames are delivered
// to it.
func TestPipeManager_OutputTapNeedsSubscriber(t *testing.T) {
	skipIfNoTmuxBinary(t)
	name := createTestSessionStrict(t, "outdemand")
	bus := events.Default()
	dir := bus.Stats().Dir
	if dir == "" {
		t.Skip("event bus disabled")
	}

	var callbacks atomic.Int64
	pm := NewPipeManager(context.Background(), func(s string) {
		if s == name {
			callbacks.Add(1)
		}
	})
	defer pm.Close()
	if err := pm.Connect(name, ""); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !waitFor(2*time.Second, func() bool { return pm.IsConnected(name) }) {
		t.Fatal("pipe never connected")
	}

	const n = 20
	// One command at a time: output events coalesce while unread, so each
	// step waits for the live onOutput consumer to see its own event.
	for i := 0; i < n; i++ {
		before := callbacks.Load()
		_ = exec.Command("tmux", "send-keys", "-t", name, fmt.Sprintf("echo no-follower-%d", i), "Enter").Run()
		if !waitFor(5*time.Second, func() bool { return callbacks.Load() > before }) {
			t.Fatalf("live onOutput consumer missed output event %d", i)
		}
	}
	// The tap queue is FIFO: once this marker is on disk every tmux.output
	// published before it would be too.
	events.PublishDefault("test.marker", name, nil)
	if !waitFor(5*time.Second, func() bool { _, m := persistedOutputFrames(t, dir, name); return m == 1 }) {
		t.Fatal("marker frame never persisted")
	}
	if got, _ := persistedOutputFrames(t, dir, name); got != 0 {
		t.Fatalf("no follower: %d tmux.output frames written for %d output events, want 0", got, callbacks.Load())
	}
	t.Logf("no follower: %d output events, 0 tmux.output frames written", callbacks.Load())

	// A follower that wants tmux.output receives it.
	release := bus.Want(events.KindTmuxOutput)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, bus.Cursor())
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; delivered < n; {
		select {
		case f, ok := <-sub.Frames():
			if !ok {
				t.Fatalf("subscription ended after %d/%d frames: %v", delivered, n, sub.Err())
			}
			if f.Kind == events.KindTmuxOutput && f.SessionID == name {
				delivered++
			}
		case <-tick.C:
			_ = exec.Command("tmux", "send-keys", "-t", name, fmt.Sprintf("echo follower-%d", i), "Enter").Run()
			i++
		}
	}
	written, _ := persistedOutputFrames(t, dir, name)
	t.Logf("with follower: %d tmux.output frames delivered, %d written", delivered, written)
}
