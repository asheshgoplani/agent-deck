package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests in this file cover the OUT-OF-BAND half of the protocol-mismatch
// fix: the liveness probes stay on cmd.Run() with no stdio to capture, and the
// one exchange that must read tmux's error text to classify it is settled once
// per socket per TTL.
//
// Why that split matters, and why these tests assert on timing: socket.go
// documents the EOF hang where a forked child of tmux inherits the subprocess's
// output descriptors and never closes them (the tmux server's terminal
// pass-through dups under bridged stdio — Claude Code /remote-control, ssh
// ControlMaster). cmd.Output()/CombinedOutput() then wait for an EOF that never
// arrives, bounded only by cmd.WaitDelay (tmuxSubprocessWaitDelay = 2s). Doing
// that capture inside Session.Exists() would put up to 2s on the status hot
// path, per probe. Run() with nil stdio has no pipe and no copy goroutine, so
// there is nothing to wait for at all.

// TestSocketHasProtocolMismatch_ClassifiesOncePerSocketPerTTL pins the caching
// contract: the verdict is a property of the client/server binary pair, so it is
// settled once per socket and reused, and two sockets never share a verdict.
func TestSocketHasProtocolMismatch_ClassifiesOncePerSocketPerTTL(t *testing.T) {
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	var mu sync.Mutex
	calls := map[string]int{}
	t.Cleanup(setSocketMismatchProbeForTest(func(socket string) bool {
		mu.Lock()
		defer mu.Unlock()
		calls[socket]++
		return socket == "mismatched-socket"
	}))

	for i := 0; i < 5; i++ {
		if !socketHasProtocolMismatch("mismatched-socket") {
			t.Fatalf("call %d: mismatched socket reported as matching", i)
		}
		if socketHasProtocolMismatch("healthy-socket") {
			t.Fatalf("call %d: healthy socket reported as mismatched", i)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["mismatched-socket"] != 1 || calls["healthy-socket"] != 1 {
		t.Fatalf("classifier ran %v; want exactly 1 per socket within the TTL — "+
			"a per-probe classification is what put a stdio capture on the hot path",
			calls)
	}
}

// TestSocketHasProtocolMismatch_SingleFlightsConcurrentCallers guards the burst
// case: every session on a wedged socket reads "gone" at the same time on a
// status pass, and that burst must cost ONE classification, not one per session.
func TestSocketHasProtocolMismatch_SingleFlightsConcurrentCallers(t *testing.T) {
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	t.Cleanup(setSocketMismatchProbeForTest(func(string) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release // hold the winner inside the probe so the losers must queue
		return true
	}))

	const callers = 16
	var wg sync.WaitGroup
	verdicts := make([]bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			verdicts[i] = socketHasProtocolMismatch("busy-socket")
		}(i)
	}
	// Give the goroutines time to pile up on the single-flight lock, then let the
	// in-flight classification finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("classifier ran %d times for %d concurrent callers on one socket; want 1", got, callers)
	}
	for i, v := range verdicts {
		if !v {
			t.Fatalf("caller %d got verdict false; every caller must see the single-flighted result", i)
		}
	}
}

// TestSocketHasProtocolMismatch_ReclassifiesAfterTTL is the other half of the
// caching contract: a stale verdict must not pin a socket forever. The direction
// that matters is a mismatch appearing while "no mismatch" is still cached —
// sessions there read gone until the entry expires.
func TestSocketHasProtocolMismatch_ReclassifiesAfterTTL(t *testing.T) {
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	var mu sync.Mutex
	calls := 0
	mismatched := false
	t.Cleanup(setSocketMismatchProbeForTest(func(string) bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return mismatched
	}))

	if socketHasProtocolMismatch("aging-socket") {
		t.Fatal("first classification should report no mismatch")
	}

	// Age the entry past the TTL, then flip what the classifier would say.
	socketMismatchMu.Lock()
	aged := socketMismatchCache["aging-socket"]
	aged.checkedAt = time.Now().Add(-2 * socketMismatchCacheTTL)
	socketMismatchCache["aging-socket"] = aged
	socketMismatchMu.Unlock()
	mu.Lock()
	mismatched = true
	mu.Unlock()

	if !socketHasProtocolMismatch("aging-socket") {
		t.Fatal("expired entry was reused; a mismatch that appears later must be picked up")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("classifier ran %d times; want 2 (once before the TTL, once after)", calls)
	}
}

// TestSession_Exists_ClassificationIsAmortizedAcrossSessionsOnASocket is the
// same contract observed through the real code path, with a fake tmux recording
// every subcommand: N sessions on a mismatched socket cost N cheap has-session
// probes and ONE classification.
func TestSession_Exists_ClassificationIsAmortizedAcrossSessionsOnASocket(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	// Fake tmux: every invocation refuses with tmux's real mismatch text, as a
	// too-old client does for every command on that socket.
	writeFakeTmux(t, dir, "printf '%s\\n' \"$*\" >> "+shellQuote(log)+"\n"+
		"echo 'protocol version mismatch (client 8, server 7)' >&2\n"+
		"exit 1\n")

	restoreTimeout := hasSessionProbeTimeout
	hasSessionProbeTimeout = 2 * time.Second
	t.Cleanup(func() { hasSessionProbeTimeout = restoreTimeout })
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	// Unique names: Exists() short-circuits on a live PipeManager connection, and
	// that manager is package-level state shared with every other test in
	// internal/tmux. A generic name that a sibling test happens to register would
	// skip the subprocess probe and break the count below for an unrelated reason.
	const socket = "agent-deck-amortized-test"
	for _, name := range []string{
		"agent-deck-amortized-alpha",
		"agent-deck-amortized-beta",
		"agent-deck-amortized-gamma",
	} {
		s := &Session{Name: name, SocketName: socket}
		if !s.Exists() {
			t.Fatalf("Exists() reported %q gone on a mismatched socket; a refusal from the "+
				"server is not evidence of absence", name)
		}
	}

	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read fake tmux log: %v", err)
	}
	var hasSession, listSessions int
	for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		switch {
		case strings.Contains(line, "has-session"):
			hasSession++
		case strings.Contains(line, "list-sessions"):
			listSessions++
		}
	}
	if hasSession != 3 {
		t.Fatalf("saw %d has-session probes, want 3 (one per session)", hasSession)
	}
	if listSessions != 1 {
		t.Fatalf("saw %d classification probes for 3 sessions on one socket, want 1: %s",
			listSessions, string(recorded))
	}
}

// TestSession_Exists_StaysInBudgetWithAChildHoldingTheOutputDescriptors is the
// end-to-end budget regression. Every tmux invocation here orphans a child that
// inherits the subprocess's output descriptors and holds them well past tmux's
// own exit — the bridged-stdio pattern socket.go documents, and the reason
// eval_smoke's TestEval_Session_StatusUnderBridgedStdio_NoHang exists.
//
// In the same build it also pins the two verdicts, so a fix for the hang cannot
// quietly trade away the classification it was added for: a live session still
// reads true, a genuine mismatch still reads indeterminate (true), and an
// authoritative "can't find session" still reads false.
func TestSession_Exists_StaysInBudgetWithAChildHoldingTheOutputDescriptors(t *testing.T) {
	// Half of tmuxSubprocessWaitDelay: comfortably above two shell forks even on
	// a loaded CI runner, and strictly below the drain any pipe-backed capture
	// would pay here. It is deliberately expressed against that constant — the
	// point of the assertion is "no stdio drain", not a wall-clock number.
	//
	// This budget applies to EVERY case, including the two that classify the
	// socket. The classification is out of band and amortized, but it still runs
	// on the caller's goroutine, so it captures tmux's stderr into an *os.File
	// rather than a pipe and has nothing to drain either.
	budget := tmuxSubprocessWaitDelay / 2

	cases := []struct {
		name       string
		reply      string // shell emitting tmux's stderr text
		exit       string
		wantExists bool
	}{
		{
			name:       "live session",
			reply:      "",
			exit:       "exit 0\n",
			wantExists: true,
		},
		{
			name:       "protocol version mismatch",
			reply:      "echo 'protocol version mismatch (client 8, server 7)' >&2\n",
			exit:       "exit 1\n",
			wantExists: true,
		},
		{
			name:       "authoritative absence",
			reply:      "echo \"can't find session: leaky\" >&2\n",
			exit:       "exit 1\n",
			wantExists: false,
		},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			// `sleep 30 &` inherits this script's stdout/stderr and keeps them open
			// for 30s — far longer than tmuxSubprocessWaitDelay — so any pipe-backed
			// capture in the code under test blocks on an EOF that never comes.
			writeFakeTmux(t, dir, "sleep 30 &\n"+c.reply+c.exit)

			restoreTimeout := hasSessionProbeTimeout
			hasSessionProbeTimeout = 2 * time.Second
			t.Cleanup(func() { hasSessionProbeTimeout = restoreTimeout })
			resetSocketMismatchCacheForTest()
			t.Cleanup(resetSocketMismatchCacheForTest)

			s := &Session{
				Name:       "leaky",
				SocketName: "agent-deck-leaky-test-" + itoa(i),
			}

			start := time.Now()
			got := s.Exists()
			elapsed := time.Since(start)

			if got != c.wantExists {
				t.Fatalf("Exists() = %v, want %v", got, c.wantExists)
			}
			if elapsed > budget {
				t.Fatalf("Exists() took %v, over its %v budget, while a child held the "+
					"output descriptors — liveness must not depend on that child closing them",
					elapsed, budget)
			}
		})
	}
}

// writeFakeTmux drops an executable `tmux` shim in dir and puts dir first on
// PATH for the duration of the test. body is /bin/sh.
func writeFakeTmux(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// shellQuote single-quotes s for embedding in the /bin/sh shims above.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
