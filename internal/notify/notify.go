// Package notify surfaces out-of-band alerts (terminal bell, desktop popup)
// when a managed session needs attention. Ported from agenthop with an added
// macOS branch (see notify_darwin.go).
package notify

import "os"

// Bell rings the controlling terminal's bell. Writing BEL to /dev/tty is safe
// inside an alt-screen TUI (it doesn't move the cursor). No-op if there is no
// controlling tty.
func Bell() {
	if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString("\a")
		_ = f.Close()
	}
}

// Desktop fires a desktop notification if the platform supports one
// (best-effort, non-blocking). Implemented per-GOOS in notify_darwin.go /
// notify_other.go.
func Desktop(title, body string) {
	desktopNotify(title, body)
}
