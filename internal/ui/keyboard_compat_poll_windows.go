//go:build windows

package ui

import "time"

// pollFdReady always returns false on Windows (no unix.Poll available).
// The lone-ESC timeout fix is inactive on Windows; ESC handling falls back
// to the original buffering behaviour.
func pollFdReady(_ int, _ time.Duration) bool {
	return false
}
