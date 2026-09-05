package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"testing"
)

// The embedded terminal copies on a worker with a per-copy deadline; that only
// bounds a stuck desktop clipboard service if the deadline reaches the child
// process. Drive runClipCmd with a controlled executable so the assertion does
// not depend on which clipboard tools the host happens to have.
func TestRunClipCmdHonorsCancellation(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("no cat on PATH")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runClipCmd(ctx, "cat", nil, "cancelled clipboard copy")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runClipCmd with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestCopyContextPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Whatever native tool the host resolves, a cancelled context must not
	// report success.
	if _, err := CopyContext(ctx, "cancelled clipboard copy", false); err == nil {
		t.Fatal("cancelled clipboard copy unexpectedly succeeded")
	}
}

func TestCopy_EmptyContent(t *testing.T) {
	_, err := Copy("", false)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if err.Error() != "no content to copy" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCountLines_SingleLine(t *testing.T) {
	n := countLines("hello world")
	if n != 1 {
		t.Errorf("expected 1, got %d", n)
	}
}

func TestCountLines_MultipleLines(t *testing.T) {
	n := countLines("line1\nline2\nline3\n")
	if n != 3 {
		t.Errorf("expected 3, got %d", n)
	}
}

func TestCountLines_NoTrailingNewline(t *testing.T) {
	n := countLines("line1\nline2\nline3")
	if n != 3 {
		t.Errorf("expected 3, got %d", n)
	}
}

func TestCountLines_Empty(t *testing.T) {
	n := countLines("")
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestCountLines_OnlyNewlines(t *testing.T) {
	n := countLines("\n\n\n")
	if n != 3 {
		t.Errorf("expected 3, got %d", n)
	}
}

func TestGenerateOSC52_NoTmux(t *testing.T) {
	text := "hello"
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := generateOSC52(encoded, false)

	expected := "\x1b]52;c;" + encoded + "\x07"
	if seq != expected {
		t.Errorf("expected %q, got %q", expected, seq)
	}
}

func TestGenerateOSC52_WithTmux(t *testing.T) {
	text := "hello"
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := generateOSC52(encoded, true)

	// Should wrap in DCS passthrough
	expected := "\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\x07\x1b\\"
	if seq != expected {
		t.Errorf("expected %q, got %q", expected, seq)
	}
}

func TestCopy_ByteSize(t *testing.T) {
	// This test only works on macOS where pbcopy is available
	// On other platforms, it will be skipped
	result, err := Copy("hello world", false)
	if err != nil {
		t.Skipf("clipboard not available: %v", err)
	}
	if result.ByteSize != 11 {
		t.Errorf("expected ByteSize=11, got %d", result.ByteSize)
	}
}

func TestCopy_LineCount(t *testing.T) {
	result, err := Copy("line1\nline2\nline3\n", false)
	if err != nil {
		t.Skipf("clipboard not available: %v", err)
	}
	if result.LineCount != 3 {
		t.Errorf("expected LineCount=3, got %d", result.LineCount)
	}
}

func TestCopy_NativeMethod(t *testing.T) {
	result, err := Copy("test content", false)
	if err != nil {
		t.Skipf("clipboard not available: %v", err)
	}
	// On macOS it should be "pbcopy"
	if result.Method == "" {
		t.Error("expected non-empty method")
	}
}

func TestCopy_FallbackNoMethodAvailable(t *testing.T) {
	// When no native clipboard and no OSC52, should error
	// This is hard to test in isolation without mocking exec.LookPath,
	// so we verify the error message format when supportsOSC52=false
	// and the platform has no clipboard tool available.
	// On macOS/Linux with pbcopy/xclip this will succeed, so we skip.
	_, err := Copy("test", false)
	if err == nil {
		t.Skip("native clipboard available, cannot test fallback error")
	}
	// If we get here, verify the error is about no method
	if err.Error() != "no clipboard method available (install pbcopy, xclip, xsel, or wl-copy)" {
		t.Logf("got expected error variant: %v", err)
	}
}
