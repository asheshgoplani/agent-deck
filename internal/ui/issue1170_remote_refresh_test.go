// Issue #1170 (by @devtechwebsource on v1.9.30): remote sessions created
// after the TUI launches never appear until quit+relaunch.
//
// The shipped code DID poll remotes every 30s on tickMsg, but two defects
// kept the list stale/flickering for the reporter's 3-federated-remote
// setup:
//
//  1. fetchRemoteSessions shared a single 15s context across ALL remotes
//     fetched sequentially, so one slow/offline remote starved the others
//     and they dropped out of the result map.
//  2. the result handler did a wholesale `h.remoteSessions = msg.sessions`,
//     so any remote missing from a partial/failed fetch had its sessions
//     wiped — last-good data was lost on every transient SSH hiccup.
//
// The fix: per-remote timeouts + a `failed` set on the fetch message, and a
// merge that keeps last-good sessions for a remote that errored this tick
// while still dropping sessions/remotes that genuinely went away. Plus a
// configurable, tighter poll interval (issue #1170 / GetRemoteSessionRefreshSecs).
package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteInfo(remote, id, title, status string) session.RemoteSessionInfo {
	return session.RemoteSessionInfo{ID: id, Title: title, Tool: "claude", Status: status, RemoteName: remote}
}

// --- Periodic poll gating (the "re-fetch on tick" mechanism, scenario 1) ---

// TestIssue1170_ShouldFetch_WhenStale: once the configured interval elapses,
// the tick must trigger a re-fetch. Without this the list freezes at the
// startup snapshot — the headline symptom.
func TestIssue1170_ShouldFetch_WhenStale(t *testing.T) {
	home := NewHome()
	home.remoteSessionRefreshSec = 15
	home.lastRemoteFetch = time.Now().Add(-30 * time.Second)
	home.remotesFetchActive = false
	if !home.shouldFetchRemoteSessions(time.Now()) {
		t.Fatal("expected a stale remote list (>interval) to trigger a re-fetch")
	}
}

// TestIssue1170_ShouldFetch_SkipsWhenFresh: don't hammer SSH — a fetch that
// just happened must not re-fire until the interval elapses.
func TestIssue1170_ShouldFetch_SkipsWhenFresh(t *testing.T) {
	home := NewHome()
	home.remoteSessionRefreshSec = 15
	home.lastRemoteFetch = time.Now()
	home.remotesFetchActive = false
	if home.shouldFetchRemoteSessions(time.Now()) {
		t.Fatal("expected a fresh fetch (<interval) to be skipped")
	}
}

// TestIssue1170_ShouldFetch_SkipsWhenActive: overlapping fetches must not
// stack — an in-flight fetch blocks the next trigger regardless of age.
func TestIssue1170_ShouldFetch_SkipsWhenActive(t *testing.T) {
	home := NewHome()
	home.remoteSessionRefreshSec = 15
	home.lastRemoteFetch = time.Now().Add(-1 * time.Hour)
	home.remotesFetchActive = true
	if home.shouldFetchRemoteSessions(time.Now()) {
		t.Fatal("expected an in-flight fetch to suppress a new trigger")
	}
}

// --- Merge behavior (happy / removed / failure / boundary) ---

// TestIssue1170_NewRemoteSession_Appears (happy path): a session created on
// the remote after startup must surface once the next fetch lands.
func TestIssue1170_NewRemoteSession_Appears(t *testing.T) {
	prev := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "existing", "running")},
	}
	fetched := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "existing", "running"), remoteInfo("dev", "r2", "fresh-session", "running")},
	}
	got := mergeRemoteSessions(prev, fetched, nil)
	if len(got["dev"]) != 2 {
		t.Fatalf("dev sessions = %d, want 2 — new remote session must appear", len(got["dev"]))
	}
}

// TestIssue1170_RemovedRemoteSession_Drops: a session closed on the remote
// must drop from the list on the next successful fetch.
func TestIssue1170_RemovedRemoteSession_Drops(t *testing.T) {
	prev := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "a", "running"), remoteInfo("dev", "r2", "b", "running")},
	}
	fetched := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "a", "running")},
	}
	got := mergeRemoteSessions(prev, fetched, nil)
	if len(got["dev"]) != 1 || got["dev"][0].ID != "r1" {
		t.Fatalf("dev sessions = %+v, want only r1 — removed session must drop", got["dev"])
	}
}

// TestIssue1170_FailedRemoteFetch_KeepsLastGood (THE fix / failure mode): a
// remote that errors on this tick must retain its last-good sessions rather
// than being wiped. This is the wholesale-replace bug that made remotes
// flicker out for the 3-remote reporter.
func TestIssue1170_FailedRemoteFetch_KeepsLastGood(t *testing.T) {
	prev := map[string][]session.RemoteSessionInfo{
		"dev":  {remoteInfo("dev", "r1", "a", "running")},
		"prod": {remoteInfo("prod", "r9", "z", "waiting")},
	}
	// dev fetched fine; prod timed out this tick.
	fetched := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "a", "running")},
	}
	failed := map[string]bool{"prod": true}

	got := mergeRemoteSessions(prev, fetched, failed)
	if len(got["prod"]) != 1 || got["prod"][0].ID != "r9" {
		t.Fatalf("prod sessions = %+v, want last-good r9 retained — a failed fetch must not wipe a remote", got["prod"])
	}
	if len(got["dev"]) != 1 {
		t.Fatalf("dev sessions = %+v, want r1 — healthy remote unaffected", got["dev"])
	}
}

// TestIssue1170_RemovedFromConfig_DropsRemote (boundary): a remote removed
// from config (absent from both fetched and failed) must disappear — we only
// keep last-good for *errored* remotes, not deconfigured ones.
func TestIssue1170_RemovedFromConfig_DropsRemote(t *testing.T) {
	prev := map[string][]session.RemoteSessionInfo{
		"dev":     {remoteInfo("dev", "r1", "a", "running")},
		"retired": {remoteInfo("retired", "r2", "b", "running")},
	}
	fetched := map[string][]session.RemoteSessionInfo{
		"dev": {remoteInfo("dev", "r1", "a", "running")},
	}
	got := mergeRemoteSessions(prev, fetched, nil)
	if _, ok := got["retired"]; ok {
		t.Fatalf("retired remote still present %+v — deconfigured remote must drop", got["retired"])
	}
}

// TestIssue1170_FailedRemote_NoPriorData (boundary): a remote that errors and
// has no last-good data simply yields no sessions — never a nil-deref panic.
func TestIssue1170_FailedRemote_NoPriorData(t *testing.T) {
	got := mergeRemoteSessions(nil, nil, map[string]bool{"prod": true})
	if len(got["prod"]) != 0 {
		t.Fatalf("prod sessions = %+v, want empty when no last-good exists", got["prod"])
	}
}

// TestIssue1170_RemoteFetchMsg_FailedRemoteKeepsLastGood wires the merge
// through the real Update handler: seed dev+prod, then deliver a fetch where
// prod failed. The rendered list must still contain prod's session.
func TestIssue1170_RemoteFetchMsg_FailedRemoteKeepsLastGood(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.refreshSessionRenderSnapshot(nil)

	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev":  {remoteInfo("dev", "r1", "a", "running")},
		"prod": {remoteInfo("prod", "r9", "z", "waiting")},
	}
	home.remoteSessionsMu.Unlock()

	model, _ := home.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{
			"dev": {remoteInfo("dev", "r1", "a", "running")},
		},
		failed: map[string]bool{"prod": true},
	})
	home = model.(*Home)

	home.remoteSessionsMu.RLock()
	prodCount := len(home.remoteSessions["prod"])
	home.remoteSessionsMu.RUnlock()
	if prodCount != 1 {
		t.Fatalf("prod sessions after failed fetch = %d, want 1 (last-good retained, #1170)", prodCount)
	}
}
