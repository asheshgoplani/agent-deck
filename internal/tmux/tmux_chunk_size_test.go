// Chunk-size invariant guard for #1793. #1793 reported a "phantom send": a
// 4095-byte Codex prompt went out as a single `send-keys -l` write followed
// by a separate Enter write (sendKeysAndEnterToTarget), and the Enter was
// silently swallowed by the target — the composer showed the pasted text but
// the turn never submitted, while the tmux command itself still exited 0.
//
// This file only proves an argv-shape invariant: sendKeysChunkedToTarget
// splits a payload into ≤1023-byte literal chunks, sends Enter as its own
// trailing call, and never drops or duplicates bytes while doing so. It
// stubs tmux out entirely (recordKeySender, see tmux_vim_mode_test.go) and
// observes only the recorded argv — no real tty, pty, or tmux server is
// involved, so it CANNOT detect whether a real line discipline or target
// application actually receives and processes the Enter. It would not fail
// if the underlying phantom-send mechanism were still present; see the
// caution comment on sendKeysChunkedToTarget in tmux.go for why splitting a
// newline-free payload into smaller writes does not, by itself, change what
// a canonical-mode tty buffers. Real pane-level delivery is covered by
// internal/integration/conductor_test.go (TestConductor_ChunkedSendDelivery),
// which drives an actual tmux pane.
package tmux

import (
	"strings"
	"testing"
)

// TestSendKeysAndEnter_4095Bytes_ChunksBelow1023 is the core #1793
// argv-invariant guard: a 4095-byte payload (one byte under the old
// 4096-byte chunk boundary) must be split into multiple ≤1023-byte literal
// writes, with the Enter keystroke delivered as its own trailing call —
// never folded into the same write as the tail of the content.
func TestSendKeysAndEnter_4095Bytes_ChunksBelow1023(t *testing.T) {
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
	// literal (-l) write whose payload stays within the chunkSize invariant.
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
			t.Fatalf("content call %d carries %d bytes, exceeds the 1023-byte chunkSize: %q", i, payloadLen, call)
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
			t.Fatalf("chunk %d carries %d bytes, exceeds the 1023-byte chunkSize: %q", i, payloadLen, call)
		}
	}
}

// TestSplitIntoChunks_PreservesUTF8RuneBoundaries guards the hard-split
// fallback path (no newline within maxSize) against cutting a multibyte
// UTF-8 rune in half. A chunk boundary landing mid-rune would hand tmux an
// invalid byte sequence in one argv and an orphan continuation byte in the
// next.
func TestSplitIntoChunks_PreservesUTF8RuneBoundaries(t *testing.T) {
	// "a" repeated up to just before the boundary, then a 3-byte rune (€,
	// U+20AC) straddling the maxSize cut point, then more filler — all on
	// one line so the newline-preferring path never engages.
	const maxSize = 16
	payload := strings.Repeat("a", maxSize-1) + "€" + strings.Repeat("b", maxSize)

	chunks := splitIntoChunks(payload, maxSize)
	if len(chunks) < 2 {
		t.Fatalf("expected payload to require multiple chunks, got %d: %q", len(chunks), chunks)
	}

	var rebuilt strings.Builder
	for i, chunk := range chunks {
		if len(chunk) > maxSize {
			t.Fatalf("chunk %d exceeds maxSize %d: %d bytes", i, maxSize, len(chunk))
		}
		if !isValidUTF8Chunk(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8, rune was split across a boundary: %q", i, chunk)
		}
		rebuilt.WriteString(chunk)
	}
	if rebuilt.String() != payload {
		t.Fatalf("reassembled content (%d bytes) does not match original payload (%d bytes)", rebuilt.Len(), len(payload))
	}
}

func isValidUTF8Chunk(s string) bool {
	return strings.ToValidUTF8(s, "") == s
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
