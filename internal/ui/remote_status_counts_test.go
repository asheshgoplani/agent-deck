package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Remote group headers must show the same running/waiting glyph counts as
// local group headers (status parity, #1864 family).
func TestRemoteStatusCounts_WholeRemote(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{Group: "a", Status: "running"},
		{Group: "a/b", Status: "waiting"},
		{Group: "c", Status: "waiting"},
		{Group: "c", Status: "stopped"},
	}
	running, waiting := remoteStatusCounts(sessions, "")
	if running != 1 || waiting != 2 {
		t.Fatalf("whole-remote counts = %d/%d, want 1/2", running, waiting)
	}
}

func TestRemoteStatusCounts_SubGroupScoped(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{Group: "a", Status: "running"},
		{Group: "a/b", Status: "waiting"},
		{Group: "ab", Status: "waiting"}, // prefix trap: "ab" is not under "a"
		{Group: "c", Status: "running"},
	}
	running, waiting := remoteStatusCounts(sessions, "a")
	if running != 1 || waiting != 1 {
		t.Fatalf("sub-group counts = %d/%d, want 1/1", running, waiting)
	}
}

func TestRemoteStatusSuffix_EmptyWhenIdle(t *testing.T) {
	if got := remoteStatusSuffix(0, 0); got != "" {
		t.Fatalf("idle suffix = %q, want empty", got)
	}
	if got := remoteStatusSuffix(2, 3); got == "" {
		t.Fatalf("active suffix should not be empty")
	}
}
