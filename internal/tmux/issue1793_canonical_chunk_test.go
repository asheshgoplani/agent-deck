// Regression test for issue #1793: a payload sized just under the old 4096
// chunk boundary (4095 bytes — one byte under the Linux tty line discipline's
// canonical-mode buffer, N_TTY_BUF_SIZE/MAX_CANON = 4096) went out as a
// SINGLE `send-keys -l` write, leaving zero headroom in the kernel's
// canonical input buffer for the Enter that follows as a separate write in
// sendKeysAndEnterToTarget. The pane's line discipline could then silently
// drop that Enter: the composer shows the pasted text but the turn never
// submits ("phantom send"), while the tmux command itself still exits 0.
//
// The fix lowers chunkSize from 4096 to 1023 so every chunk — including the
// last one immediately before the separate Enter write — leaves comfortable
// headroom in the canonical buffer. These tests record the exact argv
// sequence via the keySenderExec seam (see recordKeySender in
// tmux_vim_mode_test.go), so they run deterministically without a real tty
// or tmux server and would have failed under the old 4096 constant (a
// 4095-byte payload used to go out as exactly one literal call).
package tmux

import (
	"strings"
	"testing"
)

// TestSendKeysAndEnter_4095Bytes_SplitsBelowCanonicalLimit is the core #1793
// regression: a 4095-byte payload (one byte under the old chunk boundary)
// must now be split into multiple ≤1023-byte literal writes, with the Enter
// keystroke delivered as its own trailing call — never folded into the same
// write as the tail of the content.
func TestSendKeysAndEnter_4095Bytes_SplitsBelowCanonicalLimit(t *testing.T) {
	calls := recordKeySender(t)

	payload := strings.Repeat("x", 4095)
	s := &Session{Name: "canon1793"}
	if err := s.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	c := *calls
	if len(c) < 2 {
		t.Fatalf("expected at least 2 tmux calls (content chunk(s) + separate Enter), got %d: %v", len(c), c)
	}

	// The final call must be a bare Enter, issued as its own write.
	last := c[len(c)-1]
	if sentKey(last) != "Enter" {
		t.Fatalf("final call must be a separate Enter write, got %q", last)
	}
	if strings.Contains(last, "-l") {
		t.Fatalf("Enter must not be folded into a literal (-l) content write: %q", last)
	}

	// Every content call (everything but the trailing Enter) must be a
	// literal (-l) write whose payload never reaches the old 4096-byte
	// canonical boundary. 1023 is the new chunkSize; 4096 is the tty limit
	// this guards against.
	contentCalls := c[:len(c)-1]
	if len(contentCalls) < 2 {
		t.Fatalf("expected the 4095-byte payload to require >1 content chunk at chunkSize=1023, got %d chunk(s): %v", len(contentCalls), contentCalls)
	}
	for i, call := range contentCalls {
		if !strings.Contains(call, "-l") {
			t.Fatalf("content call %d must be a literal (-l) write: %q", i, call)
		}
		payloadLen := literalPayloadLen(t, call)
		if payloadLen > 1023 {
			t.Fatalf("content call %d carries %d bytes, exceeds the 1023-byte canonical-safe chunk size: %q", i, payloadLen, call)
		}
		if payloadLen >= 4096 {
			t.Fatalf("content call %d carries %d bytes, at/above the 4096-byte tty canonical limit this fix guards against: %q", i, payloadLen, call)
		}
	}

	// Reassembling every content chunk must reproduce the original payload
	// byte-for-byte — chunking must never drop or duplicate bytes.
	var rebuilt strings.Builder
	for _, call := range contentCalls {
		rebuilt.WriteString(literalPayload(t, call))
	}
	if rebuilt.String() != payload {
		t.Fatalf("reassembled content (%d bytes) does not match original payload (%d bytes)", rebuilt.Len(), len(payload))
	}
}

// TestSendKeysChunked_ChunkSizeBelow4096Boundary is a smaller, direct check
// that a payload straddling the old 4096-byte boundary and the new
// 1023-byte one is not sent as a single write.
func TestSendKeysChunked_ChunkSizeBelow4096Boundary(t *testing.T) {
	calls := recordKeySender(t)

	// 2000 bytes: over the new 1023-byte chunkSize, but well under the old
	// 4096-byte one — this used to go out as a single send-keys -l call.
	payload := strings.Repeat("y", 2000)
	s := &Session{Name: "canon1793b"}
	if err := s.SendKeysChunked(payload); err != nil {
		t.Fatalf("SendKeysChunked returned error: %v", err)
	}

	c := *calls
	if len(c) < 2 {
		t.Fatalf("expected a 2000-byte payload to require >1 chunk at chunkSize=1023, got %d call(s): %v", len(c), c)
	}
	for i, call := range c {
		if payloadLen := literalPayloadLen(t, call); payloadLen > 1023 {
			t.Fatalf("chunk %d carries %d bytes, exceeds the 1023-byte canonical-safe chunk size: %q", i, payloadLen, call)
		}
	}
}

// literalPayload extracts the text after the `--` marker in a recorded
// `send-keys -l -t <target> -- <text>` argv string. Reconstructing via
// strings.Join(fields, " ") in the caller would corrupt embedded spaces, so
// this locates the marker in the pre-joined argv fields directly.
func literalPayload(t *testing.T, call string) string {
	t.Helper()
	const marker = "-- "
	idx := strings.Index(call, marker)
	if idx == -1 {
		t.Fatalf("recorded call has no literal `--` marker: %q", call)
	}
	return call[idx+len(marker):]
}

func literalPayloadLen(t *testing.T, call string) int {
	t.Helper()
	return len(literalPayload(t, call))
}
