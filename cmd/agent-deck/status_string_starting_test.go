package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestStatusString_Starting pins that a booting session is reported as
// "starting", not "unknown", so list --json / session show say what is
// happening and --defer-if-busy / --wait treat a booting target as busy.
func TestStatusString_Starting(t *testing.T) {
	got := StatusString(session.StatusStarting)
	if got != "starting" {
		t.Fatalf("StatusString(StatusStarting) = %q, want %q", got, "starting")
	}
	if !send.StatusIsBusy(got) {
		t.Fatalf("send.StatusIsBusy(%q) = false; a booting target must count as busy for --defer-if-busy", got)
	}
	if r := classifyBusyStatus(got); r != busyProbeBusy {
		t.Fatalf("classifyBusyStatus(%q) = %v, want busyProbeBusy", got, r)
	}
}

// TestStatusString_NoDeclaredStatusIsUnknown guards against a new Status
// constant being added without a StatusString case.
func TestStatusString_NoDeclaredStatusIsUnknown(t *testing.T) {
	for _, st := range []session.Status{
		session.StatusRunning,
		session.StatusWaiting,
		session.StatusIdle,
		session.StatusError,
		session.StatusStarting,
		session.StatusStopped,
		session.StatusQueued,
	} {
		if got := StatusString(st); got != string(st) {
			t.Errorf("StatusString(%q) = %q, want %q", st, got, string(st))
		}
	}
}
