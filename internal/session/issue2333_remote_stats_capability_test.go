package session

import (
	"bytes"
	"context"
	"flag"
	"testing"
)

// #2333: PR #2331's `list --json --stats` was sent unconditionally, which
// v1.16.13 (every remote deployed at the time) rejects outright via Go's
// flag package — no version negotiation, no fallback, breaking polling of
// every remote fleet-wide until each one's binary caught up. These tests
// pin the fix: an old remote's rejection is detected and retried without
// the flag instead of failing the poll, a new remote is asked normally, and
// the capability verdict is invalidated when the remote's version changes
// so an upgraded remote is re-probed rather than staying pinned to "no
// stats" forever.

// realFlagRejectionError is exactly the error text SSHRunner.run() produces
// for a real remote rejecting --stats (reproduced against agentbox
// v1.16.13 in the #2333 review): Go's flag package writes usage to stderr
// and exits 2, and run() folds that stderr into the error string.
const realFlagRejectionError = `ssh command failed: exit status 2: flag provided but not defined: -stats
Usage of list:
  -all
  -include-superseded
  -json`

func TestSSHRunner_FetchSessions_OldRemoteFlagRejectionFallsBackAndSucceeds(t *testing.T) {
	setupSessionXDGPathEnv(t)
	sessionsJSON := []byte(`[{"id":"s1","title":"t","status":"waiting"}]`)

	var calls [][]string
	runner := &SSHRunner{name: "agentbox-old", fetchSessionsFn: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string(nil), args...))
		for _, a := range args {
			if a == ListStatsFlag {
				return nil, nil, errFlagRejection(t)
			}
		}
		return sessionsJSON, nil, nil
	}}

	sessions, stats, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v, want the plain-call fallback to succeed", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions)=%d, want 1", len(sessions))
	}
	if stats != nil {
		t.Fatalf("stats = %+v, want nil: an old remote never sent a stats line", stats)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want two: --stats attempted, then the plain fallback", calls)
	}

	state, ok := LoadRemoteVersions()["agentbox-old"]
	if !ok || state.StatsSupported == nil || *state.StatsSupported {
		t.Fatalf("cached state = %+v, want StatsSupported=false so the next poll skips straight to the plain call", state)
	}

	// Next poll: capability is now cached, so --stats should not even be
	// attempted — this is what keeps an old remote polling fine forever,
	// not just recovering once.
	calls = nil
	sessions, stats, err = runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions (second poll): %v", err)
	}
	if len(sessions) != 1 || stats != nil {
		t.Fatalf("second poll: sessions=%v stats=%+v", sessions, stats)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly one call once the remote is known not to support --stats", calls)
	}
	for _, a := range calls[0] {
		if a == ListStatsFlag {
			t.Fatalf("second poll still sent %s after learning the remote rejects it: %v", ListStatsFlag, calls[0])
		}
	}
}

func TestSSHRunner_FetchSessions_NewRemoteGetsStats(t *testing.T) {
	setupSessionXDGPathEnv(t)
	sessionsJSON := []byte(`[{"id":"s1","title":"t","status":"waiting"}]`)

	runner := &SSHRunner{name: "g14-new", fetchSessionsFn: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		return sessionsJSON, []byte(ListStatsPrefix + `{"status_pass_ms":100,"tmux_calls":5,"sessions":1}`), nil
	}}

	sessions, stats, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions)=%d, want 1", len(sessions))
	}
	if stats == nil || stats.StatusPassMS != 100 {
		t.Fatalf("stats = %+v, want the parsed stats line", stats)
	}

	state, ok := LoadRemoteVersions()["g14-new"]
	if !ok || state.StatsSupported == nil || !*state.StatsSupported {
		t.Fatalf("cached state = %+v, want StatsSupported=true", state)
	}
}

func TestSSHRunner_RemoteStatsCapability_InvalidatesOnVersionChange(t *testing.T) {
	setupSessionXDGPathEnv(t)

	// Seed the cache as if this remote was probed at v1.16.13 and found not
	// to support --stats.
	if err := RecordRemoteVersions(map[string]RemoteVersionState{
		"sbbox": {Version: "1.16.13", Found: true},
	}); err != nil {
		t.Fatalf("RecordRemoteVersions (seed): %v", err)
	}
	if err := RecordRemoteStatsSupport("sbbox", "1.16.13", false); err != nil {
		t.Fatalf("RecordRemoteStatsSupport: %v", err)
	}
	runner := &SSHRunner{name: "sbbox"}
	if runner.remoteSupportsStats() {
		t.Fatalf("remoteSupportsStats() = true at v1.16.13, want false (cached rejection)")
	}

	// The remote upgrades: the hourly version check (home.go's
	// recordRemoteVersions -> RecordRemoteVersions) reports a new version
	// with no opinion on StatsSupported, exactly like the real call site
	// does (msg.versions only ever sets Version/Found/CheckedAt).
	if err := RecordRemoteVersions(map[string]RemoteVersionState{
		"sbbox": {Version: "1.17.0", Found: true},
	}); err != nil {
		t.Fatalf("RecordRemoteVersions (upgrade): %v", err)
	}

	state := LoadRemoteVersions()["sbbox"]
	if state.StatsSupported != nil {
		t.Fatalf("StatsSupported = %v after a version bump, want nil (re-probe required)", *state.StatsSupported)
	}
	if !runner.remoteSupportsStats() {
		t.Fatalf("remoteSupportsStats() = false right after an upgrade, want true (optimistic re-probe)")
	}

	// The upgraded remote now accepts --stats; FetchSessions should record
	// the new verdict against the new version, not the stale one.
	sessionsJSON := []byte(`[]`)
	runner.fetchSessionsFn = func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		return sessionsJSON, []byte(ListStatsPrefix + `{"status_pass_ms":1,"tmux_calls":1,"sessions":0}`), nil
	}
	if _, stats, err := runner.FetchSessions(context.Background()); err != nil || stats == nil {
		t.Fatalf("FetchSessions after upgrade: stats=%+v err=%v", stats, err)
	}

	state = LoadRemoteVersions()["sbbox"]
	if state.Version != "1.17.0" || state.StatsSupported == nil || !*state.StatsSupported {
		t.Fatalf("state after re-probe = %+v, want version 1.17.0 with StatsSupported=true", state)
	}
}

type stubError string

func (e stubError) Error() string { return string(e) }

func errFlagRejection(t *testing.T) error {
	t.Helper()
	return stubError(realFlagRejectionError)
}

func TestIsStatsFlagRejected(t *testing.T) {
	if !isStatsFlagRejected(stubError(realFlagRejectionError)) {
		t.Fatalf("isStatsFlagRejected(real rejection text) = false, want true")
	}
	if isStatsFlagRejected(nil) {
		t.Fatalf("isStatsFlagRejected(nil) = true, want false")
	}
	if isStatsFlagRejected(stubError("ssh command failed: connection timed out")) {
		t.Fatalf("isStatsFlagRejected(unrelated network error) = true, want false: must not swallow real failures")
	}
}

// TestIsStatsFlagRejected_RealFlagPackageRejection exercises Go's actual
// flag package (the same mechanics v1.16.13's `list` command hits: an
// unregistered "-stats"/"--stats"), not a hand-typed string, so the
// detection logic is pinned to real behavior rather than a guess at its
// wording. This is exactly the gap the #2333 review flagged in the
// original test suite: every existing stats test stubbed stdout/stderr
// directly and never exercised a real flag-package rejection.
func TestIsStatsFlagRejected_RealFlagPackageRejection(t *testing.T) {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(new(bytes.Buffer)) // usage text goes into the error, not stderr, for this test
	fs.Bool("all", false, "")
	fs.Bool("include-superseded", false, "")
	fs.Bool("json", false, "")
	err := fs.Parse([]string{ListStatsFlag})
	if err == nil {
		t.Fatalf("fs.Parse(%q) succeeded against a flag set that never registered it", ListStatsFlag)
	}
	if !isStatsFlagRejected(err) {
		t.Fatalf("isStatsFlagRejected(%v) = false, want true", err)
	}
}
