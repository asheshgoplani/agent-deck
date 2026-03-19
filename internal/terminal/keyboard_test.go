package terminal

import (
	"bytes"
	"testing"
)

func TestDisableKittyKeyboard(t *testing.T) {
	var buf bytes.Buffer
	DisableKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[>0u\x1b[>4;0m"
	if got != want {
		t.Errorf("DisableKittyKeyboard wrote %q, want %q", got, want)
	}
}

func TestRestoreKittyKeyboard(t *testing.T) {
	var buf bytes.Buffer
	RestoreKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[<u\x1b[>4;1m"
	if got != want {
		t.Errorf("RestoreKittyKeyboard wrote %q, want %q", got, want)
	}
}

// TestDisableKittyKeyboardIdempotent verifies calling disable twice produces
// the same sequences (important for the detach-cleanup use case where disable
// is called even if the terminal is already in legacy mode).
func TestDisableKittyKeyboardIdempotent(t *testing.T) {
	var buf bytes.Buffer
	DisableKittyKeyboard(&buf)
	DisableKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[>0u\x1b[>4;0m\x1b[>0u\x1b[>4;0m"
	if got != want {
		t.Errorf("double DisableKittyKeyboard wrote %q, want %q", got, want)
	}
}

// TestUIDelegatesMatchTerminalPackage verifies the escape sequences are
// consistent — the ui package delegates to terminal, so calling disable
// then restore should produce the expected combined output.
func TestDisableRestoreRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	DisableKittyKeyboard(&buf)
	RestoreKittyKeyboard(&buf)
	got := buf.String()
	want := "\x1b[>0u\x1b[>4;0m\x1b[<u\x1b[>4;1m"
	if got != want {
		t.Errorf("disable+restore wrote %q, want %q", got, want)
	}
}
