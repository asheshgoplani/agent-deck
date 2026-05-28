package session

import (
	"strings"
	"testing"
)

// Issue #1214 STEP 1: the transition-notifier unit must keep a hard recycle
// backstop (RuntimeMaxSec) on top of Restart=always so the daemon can never run
// stale code even if the in-process version watcher is bypassed. Guard the
// template so a future edit can't silently drop the backstop.
func TestSystemdTransitionNotifierHasRecycleBackstop(t *testing.T) {
	tmpl := systemdTransitionNotifierServiceTemplate
	if !strings.Contains(tmpl, "RuntimeMaxSec=") {
		t.Fatalf("transition-notifier unit lost its RuntimeMaxSec recycle backstop:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "Restart=always") {
		t.Fatalf("transition-notifier unit must still Restart=always so the recycle brings it back:\n%s", tmpl)
	}
}
