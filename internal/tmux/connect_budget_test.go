package tmux

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A session whose control pipe cannot be opened (the 2026-09-19 switch-fresh
// session: handshake error on every attempt, ~1000 log lines a minute from
// the reconciler, the tick fallback and the reviver all calling Connect)
// gets a per-session budget: attempts back off from 2s to a minute, refused
// attempts are counted, and one summary line a minute replaces the storm.
func TestConnectBudget_BacksOffAndLogsOnce(t *testing.T) {
	clock := time.Date(2026, 9, 19, 14, 25, 0, 0, time.UTC)
	var logBuf bytes.Buffer
	b := newConnectBudget(func() time.Time { return clock }, slog.New(slog.NewTextHandler(&logBuf, nil)))
	const s = "agentdeck_switch-fresh_0c1970fd"

	if err := b.allow(s); err != nil {
		t.Fatalf("first attempt must be allowed: %v", err)
	}
	b.failed(s, errors.New("session x: 0"))
	// Refused for the next 2s, allowed after.
	if err := b.allow(s); !errors.Is(err, ErrPipeConnectBackoff) {
		t.Fatalf("expected backoff, got %v", err)
	}
	clock = clock.Add(2 * time.Second)
	if err := b.allow(s); err != nil {
		t.Fatalf("after 2s the attempt is allowed: %v", err)
	}
	b.failed(s, errors.New("session x: 0"))
	clock = clock.Add(3 * time.Second)
	if err := b.allow(s); !errors.Is(err, ErrPipeConnectBackoff) {
		t.Fatal("second failure backs off 4s")
	}
	// Ten more refused attempts inside the same minute: still one line.
	for i := 0; i < 10; i++ {
		clock = clock.Add(50 * time.Millisecond)
		_ = b.allow(s)
	}
	if n := strings.Count(logBuf.String(), "pipe_connect_suppressed"); n != 1 {
		t.Fatalf("summary lines = %d, want 1:\n%s", n, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "failures=1") || !strings.Contains(logBuf.String(), "session x: 0") {
		t.Fatalf("summary names the failure count and the last error:\n%s", logBuf.String())
	}
	// A minute later the next refusal logs again, with the refused count.
	clock = clock.Add(time.Minute)
	b.failed(s, errors.New("session x: 0")) // third failure: 8s
	_ = b.allow(s)
	if n := strings.Count(logBuf.String(), "pipe_connect_suppressed"); n != 2 {
		t.Fatalf("summary lines = %d, want 2:\n%s", n, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "refused=12") {
		t.Fatalf("second summary carries the refused count since the first:\n%s", logBuf.String())
	}

	// The pause caps at a minute however many failures.
	for i := 0; i < 10; i++ {
		b.failed(s, errors.New("session x: 0"))
	}
	e := b.entries[s]
	if got := e.until.Sub(clock); got != connectBackoffMax {
		t.Fatalf("cap = %v, want %v", got, connectBackoffMax)
	}

	// Success clears the budget and says so once.
	clock = clock.Add(2 * time.Minute)
	if err := b.allow(s); err != nil {
		t.Fatal(err)
	}
	b.succeeded(s)
	if _, ok := b.entries[s]; ok {
		t.Fatal("entry cleared on success")
	}
	if !strings.Contains(logBuf.String(), "pipe_connect_recovered") {
		t.Fatalf("recovery logged:\n%s", logBuf.String())
	}
	// A session that never failed never logs.
	if err := b.allow("healthy"); err != nil {
		t.Fatal(err)
	}
	b.succeeded("healthy")
	if strings.Contains(logBuf.String(), "healthy") {
		t.Fatal("healthy sessions leave no trace")
	}
}

func TestConnectBackoff(t *testing.T) {
	cases := map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 5: 32 * time.Second, 6: connectBackoffMax, 40: connectBackoffMax}
	for failures, want := range cases {
		if got := connectBackoff(failures); got != want {
			t.Errorf("connectBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}
