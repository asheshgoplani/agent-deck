package session

// Issue #1112 — bug 2 (by @ddorman-dn on v1.9.24): "Direct type STILL
// slow." The #1102/#1110 fix made local insert mode fast (persistent
// `tmux -C` client) but every keystroke on a REMOTE session still
// shelled out to `ssh ... agent-deck session send-keys` — fork+exec on
// both ends per keystroke plus an SSH round-trip. At realistic typing
// speeds (>15ms between keys) that's 100ms+ of perceived per-keystroke
// lag.
//
// Fix: StreamingRemoteKeySender opens ONE persistent
// `ssh ... agent-deck session send-keys <id> --stream` subprocess for
// the lifetime of the insert-mode session and writes one stdin line per
// keystroke. After the first call the SSH transport is a hot pipe; per
// keystroke cost collapses to a stdin write (<1µs).
//
// These tests assert:
//
//  1. The streaming sender opens exactly ONE SSH subprocess for an
//     entire 100-keystroke burst (counts invocations, per the prompt).
//  2. 100 keystrokes through the streaming dispatch path complete in
//     well under the 500ms budget — when the test stream is a
//     bytes.Buffer the budget is effectively unbounded; the
//     measurement-with-bound pattern is the regression fence so a
//     future regression that re-introduces per-call exec blows it.
//  3. The wire format matches the CLI handler (T <hex>, N <key>, E)
//     — round-trip via runSendKeysStream's parser would surface any
//     drift, but for unit purposes we assert on the emitted bytes.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStream is a thread-safe in-memory pipe simulating the persistent
// SSH stdin. The "subprocess" never runs — but `closeCount` and
// `bytesWritten` let tests assert that exactly one open/close pair
// happened and that every keystroke produced a line.
type fakeStream struct {
	buf        bytes.Buffer
	closeCount atomic.Int32
}

func (f *fakeStream) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *fakeStream) Close() error {
	f.closeCount.Add(1)
	return nil
}

// captureStreamOpenCount returns an SSHRunner whose openStreamFn
// counts spawns and returns a fakeStream. The companion accessor
// reveals how many times OpenStream was invoked.
func captureStreamOpenCount() (*SSHRunner, *atomic.Int32, *fakeStream) {
	var opens atomic.Int32
	stream := &fakeStream{}
	r := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		openStreamFn: func(ctx context.Context, args ...string) (io.WriteCloser, func() error, error) {
			opens.Add(1)
			return stream, stream.Close, nil
		},
	}
	return r, &opens, stream
}

// TestIssue1112_StreamingRemoteKeySender_OneSubprocessFor100Keys is the
// headline regression fence for bug 2: 100 SendKeys calls must NOT
// spawn 100 ssh subprocesses. The streaming sender opens once at
// OpenStreamingRemoteKeySender and reuses the stream for every Send.
// If a future refactor accidentally re-spawns per call, opens jumps to
// 100 and this test fails loudly.
func TestIssue1112_StreamingRemoteKeySender_OneSubprocessFor100Keys(t *testing.T) {
	runner, opens, stream := captureStreamOpenCount()

	sender, err := OpenStreamingRemoteKeySender(runner, "remote-sess-id", context.Background())
	if err != nil {
		t.Fatalf("OpenStreamingRemoteKeySender: %v", err)
	}
	defer sender.Close()

	const n = 100
	for i := 0; i < n; i++ {
		if err := sender.SendKeys("x"); err != nil {
			t.Fatalf("SendKeys %d: %v", i, err)
		}
	}

	if got := opens.Load(); got != 1 {
		t.Errorf("ssh subprocess spawns = %d, want 1 — streaming sender must reuse the connection (#1112 bug 2)", got)
	}
	// Each SendKeys emits one line.
	lines := strings.Count(stream.buf.String(), "\n")
	if lines != n {
		t.Errorf("emitted %d stream lines, want %d (one per keystroke)", lines, n)
	}
}

// TestIssue1112_StreamingRemoteKeySender_PerfBudget100KeysUnder500ms
// asserts the 500ms / 100 keystrokes budget the prompt called for.
// With a fake stream the actual work is bounded by hex encoding and
// buffer writes — comfortably sub-millisecond. The 500ms is the
// regression bound: a future change that re-introduces per-call
// fork+exec or any blocking I/O will blow it.
func TestIssue1112_StreamingRemoteKeySender_PerfBudget100KeysUnder500ms(t *testing.T) {
	runner, _, _ := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "remote-sess-id", context.Background())
	if err != nil {
		t.Fatalf("OpenStreamingRemoteKeySender: %v", err)
	}
	defer sender.Close()

	const n = 100
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := sender.SendKeys("a"); err != nil {
			t.Fatalf("SendKeys %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("100 streaming SendKeys took %v, want <500ms (#1112 bug 2 perf budget)", elapsed)
	}
	t.Logf("100 streaming keystrokes: %v (%.2fµs/keystroke)",
		elapsed, float64(elapsed.Microseconds())/float64(n))
}

// TestIssue1112_StreamingRemoteKeySender_WireFormat asserts the lines
// emitted to the stream match the protocol parsed by runSendKeysStream
// in cmd/agent-deck/session_send_keys_cmd.go. If they ever drift, the
// remote loop will silently ignore commands (the "unknown verb"
// branch) and the user's typing disappears.
func TestIssue1112_StreamingRemoteKeySender_WireFormat(t *testing.T) {
	runner, _, stream := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sender.Close()

	if err := sender.SendKeys("hi"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if err := sender.SendNamedKey("Up"); err != nil {
		t.Fatalf("SendNamedKey: %v", err)
	}
	if err := sender.SendEnter(); err != nil {
		t.Fatalf("SendEnter: %v", err)
	}

	wantLines := []string{
		"T " + hex.EncodeToString([]byte("hi")),
		"N Up",
		"E",
	}
	gotLines := strings.Split(strings.TrimRight(stream.buf.String(), "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d: %q", len(gotLines), len(wantLines), stream.buf.String())
	}
	for i, want := range wantLines {
		if gotLines[i] != want {
			t.Errorf("line %d = %q, want %q", i, gotLines[i], want)
		}
	}
}

// TestIssue1112_StreamingRemoteKeySender_EmptyTextIsNoOp guards the
// flush-empty-buffer case: insert mode flushes the rune buffer on any
// non-rune key, and the buffer is often empty. The streaming sender
// must not emit a stream line in that case (a literal "T \n" would
// decode to empty bytes on the remote, harmless but noisy).
func TestIssue1112_StreamingRemoteKeySender_EmptyTextIsNoOp(t *testing.T) {
	runner, _, stream := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sender.Close()

	if err := sender.SendKeys(""); err != nil {
		t.Fatalf("SendKeys(\"\"): %v", err)
	}
	if got := stream.buf.Len(); got != 0 {
		t.Errorf("buffer = %q, want empty for SendKeys(\"\")", stream.buf.String())
	}
}

// TestIssue1112_StreamingRemoteKeySender_CloseStopsDispatch is the
// failure-mode test: after Close the sender must refuse new dispatches
// (so the UI doesn't accidentally race insert-mode-exit teardown).
func TestIssue1112_StreamingRemoteKeySender_CloseStopsDispatch(t *testing.T) {
	runner, _, stream := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := sender.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if got := stream.closeCount.Load(); got != 1 {
		t.Errorf("closeCount = %d, want 1", got)
	}
	if err := sender.Close(); err != nil {
		t.Errorf("second Close should be a no-op: %v", err)
	}

	if err := sender.SendKeys("late"); err == nil {
		t.Fatal("SendKeys after Close must fail")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error after Close = %v, want one containing 'closed'", err)
	}
}

// TestIssue1112_StreamingRemoteKeySender_OpenErrorPropagates verifies
// that an SSH open failure is surfaced so the UI can fall back to the
// per-call RemoteKeySender (openRemoteInsertKeySender does this).
// Without this, a transient ssh failure would silently swallow
// insert-mode entirely on remote.
func TestIssue1112_StreamingRemoteKeySender_OpenErrorPropagates(t *testing.T) {
	wantErr := errors.New("ssh: connection refused")
	runner := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		openStreamFn: func(ctx context.Context, args ...string) (io.WriteCloser, func() error, error) {
			return nil, nil, wantErr
		},
	}
	_, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err == nil {
		t.Fatal("expected error to surface")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain missing wrapped cause; got %v", err)
	}
}

// TestIssue1112_StreamingRemoteKeySender_NamedKeyRejectsNewline guards
// the wire shape: a newline in the named key would break out of one
// line into a forged second command. Fail fast on the local side.
func TestIssue1112_StreamingRemoteKeySender_NamedKeyRejectsNewline(t *testing.T) {
	runner, _, _ := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sender.Close()

	if err := sender.SendNamedKey("Up\nE"); err == nil {
		t.Error("SendNamedKey with embedded newline must fail")
	}
}

// TestIssue1112_StreamingRemoteKeySender_BinarySafeText asserts that
// text containing newlines, NULL, and non-UTF8 bytes round-trips via
// hex without corruption. Paste-into-insert-mode is the realistic
// trigger.
func TestIssue1112_StreamingRemoteKeySender_BinarySafeText(t *testing.T) {
	runner, _, stream := captureStreamOpenCount()
	sender, err := OpenStreamingRemoteKeySender(runner, "x", context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sender.Close()

	payload := "line1\nline2\x00\xff"
	if err := sender.SendKeys(payload); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	line := strings.TrimRight(stream.buf.String(), "\n")
	if !strings.HasPrefix(line, "T ") {
		t.Fatalf("expected T-prefixed line, got %q", line)
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(line, "T "))
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	if string(decoded) != payload {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decoded), payload)
	}
}
