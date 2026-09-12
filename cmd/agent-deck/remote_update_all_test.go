package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2164: per-remote report lines and the closing summary of
// `agent-deck remote update --all`.
func TestFormatRemoteUpdateResult(t *testing.T) {
	cases := []struct {
		r    session.RemoteUpdateResult
		want string
	}{
		{session.RemoteUpdateResult{Name: "a", From: "1.15.0", To: "1.16.0", Outcome: session.RemoteUpdateOutcomeUpdated}, "✓ Updated v1.15.0 → v1.16.0"},
		{session.RemoteUpdateResult{Name: "a", To: "1.16.0", Outcome: session.RemoteUpdateOutcomeUpdated}, "✓ Installed v1.16.0"},
		{session.RemoteUpdateResult{Name: "a", From: "1.16.0", Outcome: session.RemoteUpdateOutcomeCurrent}, "✓ Up to date (v1.16.0)"},
		{session.RemoteUpdateResult{Name: "a", Outcome: session.RemoteUpdateOutcomeSkipped, Err: session.ErrRemoteBinaryMissing}, "– Skipped: agent-deck not found on remote or host unreachable"},
		{session.RemoteUpdateResult{Name: "a", Outcome: session.RemoteUpdateOutcomeFailed, Err: errors.New("deploy failed: permission denied")}, "✗ Failed: deploy failed: permission denied"},
	}
	for _, tc := range cases {
		if got := formatRemoteUpdateResult(tc.r); got != tc.want {
			t.Errorf("formatRemoteUpdateResult(%+v) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestRemoteUpdateSummary(t *testing.T) {
	results := []session.RemoteUpdateResult{
		{Name: "a", Outcome: session.RemoteUpdateOutcomeUpdated},
		{Name: "b", Outcome: session.RemoteUpdateOutcomeCurrent},
		{Name: "c", Outcome: session.RemoteUpdateOutcomeFailed, Err: errors.New("x")},
		{Name: "d", Outcome: session.RemoteUpdateOutcomeFailed, Err: errors.New("y")},
	}
	if got, want := remoteUpdateSummary(results), "4 remotes: 1 updated, 1 already current, 2 failed"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got, want := remoteUpdateSummary(results[:1]), "1 remote: 1 updated"; got != want {
		t.Errorf("single summary = %q, want %q", got, want)
	}
	if session.CountRemoteUpdateFailures(results) != 2 {
		t.Errorf("failures = %d, want 2 (drives the non-zero exit)", session.CountRemoteUpdateFailures(results))
	}
}

func TestRemoteVersionColumn(t *testing.T) {
	cases := []struct {
		state session.RemoteVersionState
		want  string
	}{
		{session.RemoteVersionState{}, "-"},
		{session.RemoteVersionState{Found: true, Version: "1.16.0"}, "v1.16.0"},
		{session.RemoteVersionState{Found: true, Version: "1.15.0"}, "v1.15.0 ↑"},
		{session.RemoteVersionState{Found: true, Version: "1.17.0"}, "v1.17.0"},
	}
	for _, tc := range cases {
		if got := remoteVersionColumn(tc.state, "1.16.0"); got != tc.want {
			t.Errorf("remoteVersionColumn(%+v) = %q, want %q", tc.state, got, tc.want)
		}
	}
}
