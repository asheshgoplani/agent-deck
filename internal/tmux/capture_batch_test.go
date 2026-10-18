package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeCaptureBatch emulates tmux running a `;` sequence of capture-pane and
// display-message commands: it stops at the first capture whose pane is in
// missing, as tmux does, returning what it printed so far.
func fakeCaptureBatch(t *testing.T, missing map[string]bool, calls *int) func(context.Context, string, []string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, args []string) ([]byte, error) {
		*calls++
		var out strings.Builder
		var cmd []string
		run := func(cmd []string) error {
			switch cmd[0] {
			case "capture-pane":
				target := cmd[2]
				if missing[target] {
					return fmt.Errorf("can't find pane: %s", target)
				}
				fmt.Fprintf(&out, "frame of %s\n\x1b[1m> \x1b[0m\n", target)
			case "display-message":
				fmt.Fprintln(&out, cmd[len(cmd)-1])
			}
			return nil
		}
		for _, a := range append(args, ";") {
			if a != ";" {
				cmd = append(cmd, a)
				continue
			}
			if err := run(cmd); err != nil {
				return []byte(out.String()), err
			}
			cmd = nil
		}
		return []byte(out.String()), nil
	}
}

func withCaptureBatchRun(t *testing.T, fn func(context.Context, string, []string) ([]byte, error)) {
	t.Helper()
	prev := captureBatchRun
	captureBatchRun = fn
	t.Cleanup(func() { captureBatchRun = prev })
}

func batchSessions(n int, socket string) []*Session {
	out := make([]*Session, n)
	for i := range out {
		out[i] = &Session{Name: fmt.Sprintf("agentdeck_batch_%d", i), SocketName: socket}
	}
	return out
}

func TestPrimePaneCapturesOneCallPerSocket(t *testing.T) {
	calls := 0
	withCaptureBatchRun(t, fakeCaptureBatch(t, nil, &calls))
	sessions := append(batchSessions(40, "batch-a"), batchSessions(3, "batch-b")...)
	if got := PrimePaneCaptures(sessions); got != len(sessions) {
		t.Fatalf("primed %d of %d sessions", got, len(sessions))
	}
	if calls != 2 {
		t.Fatalf("batched capture ran %d tmux clients for 2 sockets, want 2", calls)
	}
	for _, s := range sessions {
		got, err := s.CapturePane()
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("frame of %s\n\x1b[1m> \x1b[0m\n", s.Name); got != want {
			t.Fatalf("%s frame = %q, want %q", s.Name, got, want)
		}
	}
	if calls != 2 {
		t.Fatalf("CapturePane after priming started %d more tmux clients", calls-2)
	}
}

func TestPrimePaneCapturesChunksLargeFleets(t *testing.T) {
	calls := 0
	withCaptureBatchRun(t, fakeCaptureBatch(t, nil, &calls))
	sessions := batchSessions(captureBatchMax*2+1, "batch-a")
	if got := PrimePaneCaptures(sessions); got != len(sessions) {
		t.Fatalf("primed %d of %d", got, len(sessions))
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 chunks", calls)
	}
}

func TestPrimePaneCapturesResumesPastVanishedPane(t *testing.T) {
	calls := 0
	sessions := batchSessions(6, "batch-a")
	withCaptureBatchRun(t, fakeCaptureBatch(t, map[string]bool{sessions[2].Name: true}, &calls))
	if got := PrimePaneCaptures(sessions); got != 5 {
		t.Fatalf("primed %d, want 5 (all but the vanished pane)", got)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (abort at the vanished pane, resume after it)", calls)
	}
	sessions[2].cacheMu.RLock()
	primed := sessions[2].captureCacheFreshLocked(time.Now())
	sessions[2].cacheMu.RUnlock()
	if primed {
		t.Fatal("vanished pane must stay unprimed so its own capture reports the error")
	}
}

func TestPrimePaneCapturesSkipsSingleSession(t *testing.T) {
	calls := 0
	withCaptureBatchRun(t, fakeCaptureBatch(t, nil, &calls))
	if got := PrimePaneCaptures(batchSessions(1, "batch-a")); got != 0 || calls != 0 {
		t.Fatalf("one session: primed %d with %d calls, want no batch", got, calls)
	}
}

func TestPrimePaneCapturesFailedBatchPrimesNothing(t *testing.T) {
	withCaptureBatchRun(t, func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("no server running")
	})
	sessions := batchSessions(4, "batch-a")
	if got := PrimePaneCaptures(sessions); got != 0 {
		t.Fatalf("primed %d after a failed batch", got)
	}
}

func TestSplitCaptureBatch(t *testing.T) {
	const m = "agentdeck-capture-x-"
	out := "a\n" + m + "0\n" + m + "1\n" + "says " + m + "2\nreal\n" + m + "2\n" + "cut"
	got := splitCaptureBatch(out, m, 4)
	want := []string{"a\n", "", "says " + m + "2\nreal\n"}
	if len(got) != len(want) {
		t.Fatalf("frames = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPrimedCaptureExpires(t *testing.T) {
	s := &Session{Name: "agentdeck_expire"}
	past := time.Now().Add(-capturePrimeTTL - time.Second)
	s.primeCapture("old frame\n", past)
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if s.captureCacheFreshLocked(time.Now()) {
		t.Fatal("a primed frame past capturePrimeTTL must not answer CapturePane")
	}
}
