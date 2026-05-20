//go:build !darwin

package terminal

import (
	"errors"
	"testing"
)

// On non-darwin platforms OpenSessionInNewWindow must surface ErrUnsupported
// (not panic, not silently succeed) so the TUI can render a graceful fallback
// message instead of pretending it spawned a window.
func TestOpenSessionInNewWindow_NotSupportedOffDarwin(t *testing.T) {
	err := OpenSessionInNewWindow(AttachRequest{Name: "anything"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
