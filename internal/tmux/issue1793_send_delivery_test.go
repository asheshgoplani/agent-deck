package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Issue #1793: `session send` reported success for a payload that was never
// delivered.
//
// These tests drive a REAL tmux pane with a REAL reader and assert what the
// reader actually received. That is the whole point: the previous regression
// tests for this bug asserted the SHAPE OF THE ARGV agent-deck passes to
// `tmux send-keys` (chunk sizes), which a fix that cannot possibly work still
// satisfies — PR #1819 passed those tests while delivering nothing.
//
// Nothing here inspects argv. Every assertion is "what bytes came out of the
// other end of the pty".
// ---------------------------------------------------------------------------

// paneReader starts a tmux session whose pane runs `shellCmd` and returns the
// session, plus the path the reader writes to. The session is killed by socket
// target (never by name-matching a process, never kill-server) on cleanup.
func paneReader(t *testing.T, name, shellCmd string) (*Session, string) {
	t.Helper()
	skipIfNoTmuxBinary(t)

	dir := t.TempDir()
	out := filepath.Join(dir, "received.bin")
	full := fmt.Sprintf("%s > '%s'", shellCmd, out)

	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "200", "-y", "50", "sh", "-c", full)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create tmux session for a real-pane test: %v (%s)", err, strings.TrimSpace(string(b)))
	}
	t.Cleanup(func() {
		// Kill this one session on the socket this test binary already
		// isolated (TestMain's IsolateTmuxSocket). Never kill-server, never
		// match tmux by process name.
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	return &Session{Name: name}, out
}

// awaitDiscipline polls the pane's line discipline until it matches want,
// which doubles as the readiness gate for a pane that runs `stty` on startup.
func awaitDiscipline(t *testing.T, s *Session, wantCanonical bool) lineDiscipline {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ld, err := s.paneLineDiscipline(s.Name)
		if err == nil && ld.Canonical == wantCanonical {
			return ld
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane never reached canonical=%v (last probe error: %v)", wantCanonical, last)
	return lineDiscipline{}
}

// awaitBytes polls the reader's output file until it holds want bytes,
// returning what it holds when the deadline passes.
func awaitBytes(path string, want int, timeout time.Duration) []byte {
	deadline := time.Now().Add(timeout)
	var last []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			last = b
			if len(b) >= want {
				return b
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// TestIssue1793_RawModePane_ReceivesWholeOverLimitPayloadAndTheEnter is the
// test the maintainer asked for: drive a real pane with a payload far above
// the canonical limit and assert the pane received THE WHOLE THING and that
// it was submitted.
//
// The reader is in raw mode, which is what every real agent TUI (Claude Code,
// Codex, a readline shell prompt) puts its tty in. `head -c` exits the instant
// it has the expected byte count, so the file is flushed by process exit
// rather than by luck of stdio buffering.
func TestIssue1793_RawModePane_ReceivesWholeOverLimitPayloadAndTheEnter(t *testing.T) {
	const payloadBytes = 8000
	// +1 for the Enter: raw mode delivers it as a literal \r byte, so the
	// reader seeing it is direct evidence the submit keystroke survived the
	// body rather than being swallowed behind it.
	want := payloadBytes + 1

	sess, out := paneReader(t, "ad1793-raw", fmt.Sprintf("stty raw -echo; head -c %d", want))
	awaitDiscipline(t, sess, false)

	payload := strings.Repeat("A", payloadBytes-1) + "Z"
	if err := sess.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter(%d bytes) to a raw-mode pane must succeed: %v", payloadBytes, err)
	}

	got := awaitBytes(out, want, 15*time.Second)
	if len(got) != want {
		t.Fatalf("raw-mode pane received %d bytes, want %d (body %d + Enter): the payload was truncated in transit",
			len(got), want, payloadBytes)
	}
	if string(got[:payloadBytes]) != payload {
		t.Fatal("raw-mode pane received the right number of bytes but not the right bytes")
	}
	if got[payloadBytes] != '\r' {
		t.Fatalf("the submitting Enter never arrived after the body: trailing byte is %q", got[payloadBytes])
	}
}

// TestIssue1793_RawModePane_PreservesMultibyteRunes pins that the transport
// for over-limit payloads does not cut a UTF-8 rune in half. The old chunked
// `send-keys` path split at a raw byte offset, which put invalid UTF-8 on the
// wire whenever a multi-byte character straddled the boundary.
func TestIssue1793_RawModePane_PreservesMultibyteRunes(t *testing.T) {
	// Every rune is 3 bytes, so any split at a byte offset that is not a
	// multiple of 3 corrupts a character.
	payload := strings.Repeat("あ", 2000)
	want := len(payload) + 1

	sess, out := paneReader(t, "ad1793-utf8", fmt.Sprintf("stty raw -echo; head -c %d", want))
	awaitDiscipline(t, sess, false)

	if err := sess.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter(%d bytes of UTF-8): %v", len(payload), err)
	}

	got := awaitBytes(out, want, 15*time.Second)
	if len(got) < len(payload) || string(got[:len(payload)]) != payload {
		t.Fatalf("multibyte payload was corrupted in transit: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestIssue1793_CanonicalPane_DeliversUpToTheLimit pins the boundary from
// below: the largest line the pane's canonical buffer can hold must still go
// through untouched. Without this, "refuse over-long lines" could silently
// become "refuse everything large".
func TestIssue1793_CanonicalPane_DeliversUpToTheLimit(t *testing.T) {
	limit := canonicalBufferBytes()
	// limit includes the terminator, so the largest deliverable body is
	// limit-1 and the reader should see exactly limit bytes with the newline.
	body := strings.Repeat("A", limit-2) + "Z"

	sess, out := paneReader(t, "ad1793-canon-ok", fmt.Sprintf("head -c %d", limit))
	ld := awaitDiscipline(t, sess, true)
	if ld.MaxLine != limit {
		t.Fatalf("probe reported a %d-byte canonical buffer, this platform's is %d", ld.MaxLine, limit)
	}

	if err := sess.SendKeysAndEnter(body); err != nil {
		t.Fatalf("a %d-byte line fits the %d-byte canonical buffer and must not be refused: %v", len(body), limit, err)
	}

	got := awaitBytes(out, limit, 15*time.Second)
	if len(got) != limit || string(got[:len(body)]) != body {
		t.Fatalf("canonical pane received %d bytes, want %d: the at-limit line did not survive", len(got), limit)
	}
}

// TestIssue1793_CanonicalPane_OverLimitLineIsMeasuredLostThenRefused is the
// core of the bug and its fix, in one test.
//
// First half: send one byte too many through the RAW transport, bypassing the
// guard, and watch the kernel throw away the entire line AND the Enter. That
// is the phantom send from issue #1793 — reproduced, not assumed, on this
// machine's real line discipline.
//
// Second half: the same payload through the public send path must return
// ErrCanonicalLineOverflow and type NOTHING, so the caller reports a failure
// instead of a success and the composer is left uncorrupted.
func TestIssue1793_CanonicalPane_OverLimitLineIsMeasuredLostThenRefused(t *testing.T) {
	limit := canonicalBufferBytes()
	// One byte past what fits: body + terminator = limit + 1.
	body := strings.Repeat("A", limit)

	// --- the measurement -------------------------------------------------
	lost, lostOut := paneReader(t, "ad1793-canon-lost", fmt.Sprintf("head -c %d", limit+1))
	awaitDiscipline(t, lost, true)

	if err := lost.sendKeysToTarget(lost.Name, body); err != nil {
		t.Fatalf("raw transport send-keys: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := lost.sendEnterRawToTarget(lost.Name); err != nil {
		t.Fatalf("raw transport Enter: %v", err)
	}
	// The invariant is "this line does not arrive as a submitted line", not
	// "nothing arrives" — kernels lose it differently and both losses are the
	// #1793 phantom. macOS discards the whole overflowing line, so the reader
	// sees 0 bytes. Linux hands over the 4096 bytes it buffered and eats the
	// terminator, so the reader sees a truncated body and no Enter, and the
	// turn never starts. Asserting the macOS shape specifically is what failed
	// this test on ubuntu-latest.
	if got := awaitBytes(lostOut, len(body)+1, 3*time.Second); len(got) == len(body)+1 {
		t.Fatalf("expected a %d-byte line to be unsubmittable through a %d-byte canonical buffer, "+
			"but the reader received the whole body AND the terminator (%d bytes); "+
			"this platform's limit is not %d and the constant needs re-measuring",
			len(body), limit, len(got), limit)
	}

	// --- the fix ---------------------------------------------------------
	guarded, guardedOut := paneReader(t, "ad1793-canon-refused", fmt.Sprintf("head -c %d", limit+1))
	awaitDiscipline(t, guarded, true)

	err := guarded.SendKeysAndEnter(body)
	if err == nil {
		t.Fatal("issue #1793: a line that the pane's canonical buffer cannot hold must not report success")
	}
	if !errors.Is(err, ErrCanonicalLineOverflow) {
		t.Fatalf("want ErrCanonicalLineOverflow, got %v", err)
	}
	var overflow *CanonicalOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("the error must carry the measured numbers, got %T", err)
	}
	if overflow.LineBytes != len(body) || overflow.LimitBytes != limit {
		t.Fatalf("overflow detail wrong: line=%d limit=%d, want line=%d limit=%d",
			overflow.LineBytes, overflow.LimitBytes, len(body), limit)
	}
	if got := awaitBytes(guardedOut, 1, time.Second); len(got) != 0 {
		t.Fatalf("a refused send must type nothing, but %d bytes reached the pane", len(got))
	}
}

// TestIssue1793_CanonicalPane_ManyShortLinesAreNotRefused pins that the guard
// measures the longest LINE, not the total payload. Canonical buffering is
// per line, so a 20 KB body of short lines is perfectly deliverable and
// refusing it would be a regression.
func TestIssue1793_CanonicalPane_ManyShortLinesAreNotRefused(t *testing.T) {
	line := strings.Repeat("B", 79) + "\n"
	body := strings.Repeat(line, 250) // 20000 bytes, longest line 80
	want := len(body) + 1             // trailing Enter closes the final (empty) line

	sess, out := paneReader(t, "ad1793-canon-lines", fmt.Sprintf("head -c %d", want))
	awaitDiscipline(t, sess, true)

	if err := sess.SendKeysAndEnter(body); err != nil {
		t.Fatalf("a %d-byte payload of 80-byte lines fits a canonical buffer line-by-line: %v", len(body), err)
	}
	if got := awaitBytes(out, want, 20*time.Second); len(got) < len(body) {
		t.Fatalf("multi-line payload truncated: got %d bytes, want %d", len(got), want)
	}
}

// TestLongestLineBytes pins the quantity the guard actually measures.
func TestLongestLineBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"abc\n", 3},
		{"ab\nabcd\nx", 4},
		{"ab\nabcd\nxyzzyx", 6},
		{strings.Repeat("a", 5000), 5000},
	}
	for _, c := range cases {
		if got := longestLineBytes(c.in); got != c.want {
			t.Errorf("longestLineBytes(%d bytes) = %d, want %d", len(c.in), got, c.want)
		}
	}
}

// TestRuneSafeCut pins that the chunked fallback never splits a rune, and
// that the one case where it cannot avoid doing so still makes progress.
func TestRuneSafeCut(t *testing.T) {
	const runeWidth = 3 // "あ" is 3 bytes
	s := strings.Repeat("あ", 10)

	// Any cut with at least one whole rune behind it must land on a
	// boundary.
	for size := runeWidth; size < len(s); size++ {
		cut := runeSafeCut(s, size)
		if cut > size {
			t.Fatalf("runeSafeCut(%d) = %d: must never cut beyond the requested size", size, cut)
		}
		if cut%runeWidth != 0 {
			t.Fatalf("runeSafeCut(%d) = %d: lands mid-rune", size, cut)
		}
		if cut == 0 {
			t.Fatalf("runeSafeCut(%d) = 0: a zero-length cut would loop forever in splitIntoChunks", size)
		}
	}

	// A cut smaller than the rune it lands in has no safe answer. It must
	// still return something non-zero, or splitIntoChunks never terminates.
	for size := 1; size < runeWidth; size++ {
		if got := runeSafeCut(s, size); got != size {
			t.Fatalf("runeSafeCut(%d) = %d: with no safe cut available it must fall back to the requested size", size, got)
		}
	}

	if got := runeSafeCut("abcdef", 100); got != 6 {
		t.Fatalf("runeSafeCut past end = %d, want 6", got)
	}
	// Invalid UTF-8 must not send the backoff searching indefinitely.
	if got := runeSafeCut("\x80\x80\x80\x80\x80\x80", 4); got != 4 {
		t.Fatalf("runeSafeCut on invalid UTF-8 = %d, want the raw cut 4", got)
	}
}

// TestSplitIntoChunksNeverSplitsARune is the reconstruction guarantee for the
// chunked fallback path.
func TestSplitIntoChunksNeverSplitsARune(t *testing.T) {
	// 3-byte runes with no newline anywhere force the hard-split branch, and
	// 4096 is not a multiple of 3 so a naive cut lands mid-rune.
	original := strings.Repeat("あ", 4000)
	chunks := splitIntoChunks(original, 4096)
	if len(chunks) < 2 {
		t.Fatalf("expected a hard split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is not valid UTF-8: a rune was cut in half", i)
		}
	}
	if strings.Join(chunks, "") != original {
		t.Fatal("chunks do not reassemble into the original content")
	}
}
