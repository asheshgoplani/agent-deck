package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Messaging audit P2-1: the Stop-hook inbox drain answers with
// {decision:"block"}, which Claude Code only reads from a synchronous hook. A
// Stop entry left async by an older install must never drain (it would
// consume the parent's inbox into a reply nobody reads). Only the sync install
// exports the marker, so the handler drains only when it sees it.
func TestStopHookDrainRequiresSyncMarker(t *testing.T) {
	t.Setenv(session.StopHookSyncMarkerEnv, "")
	if stopHookIsSyncInstall() {
		t.Fatal("without the sync marker the Stop hook must not drain the inbox")
	}
	t.Setenv(session.StopHookSyncMarkerEnv, "1")
	if !stopHookIsSyncInstall() {
		t.Fatal("the sync install's marker must enable the drain")
	}
}
