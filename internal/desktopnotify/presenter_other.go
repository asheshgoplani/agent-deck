//go:build !darwin

package desktopnotify

import "fmt"

// NativePresent is intentionally unavailable away from macOS. The protocol,
// state store, and socket remain portable so regular CI validates them.
func NativePresent(Event) error {
	return fmt.Errorf("desktop notifications are supported on macOS only")
}
