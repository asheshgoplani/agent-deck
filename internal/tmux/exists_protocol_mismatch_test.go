package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIsTmuxProtocolMismatch is a pure classification test for the tmux
// "protocol version mismatch" signal (case-insensitive, substring match), and
// confirms unrelated failures are not misclassified.
func TestIsTmuxProtocolMismatch(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"canonical", "protocol version mismatch (client 8, server 7)", true},
		{"uppercased", "PROTOCOL VERSION MISMATCH (client 8, server 7)", true},
		{"embedded", "tmux: protocol version mismatch (client 8, server 7)\n", true},
		{"no server", "no server running on /tmp/tmux-1000/default", false},
		{"cant find session", "can't find session: agentdeck_a", false},
		// Adversarial: an authoritative absence reply echoes the session name, so
		// a session NAMED like the marker must NOT be classified as a mismatch —
		// absence wins over the substring.
		{"cant find session named like marker", "can't find session: protocol version mismatch", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTmuxProtocolMismatch(c.output); got != c.want {
				t.Fatalf("isTmuxProtocolMismatch(%q) = %v, want %v", c.output, got, c.want)
			}
		})
	}
}

// TestSession_Exists_ProtocolMismatchIsNotTreatedAsAbsent is the regression for
// the false-error cascade seen when the running tmux server was started by a
// newer tmux binary (e.g. one from a Nix profile) but the client's PATH resolves
// an OLDER system tmux. The old client's `has-session` probe fails fast with a
// non-zero exit, printing "protocol version mismatch (client X, server Y)". The
// old code treated any non-timeout failure as "session gone", so UpdateStatus
// flipped the still-alive session to StatusError (via terminatedPaneStatus) even
// though nothing crashed.
//
// A mismatch reply proves the server — and therefore this session — is alive:
// only the client binary is wrong. Exists() must treat it as indeterminate and
// assume the session still exists, exactly like a probe timeout.
//
// Contrast with #755 (exists_socket_test.go): a probe that COMPLETES with an
// authoritative "gone" (e.g. "can't find session") must still report false —
// covered by the sibling test below.
func TestSession_Exists_ProtocolMismatchIsNotTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	// Fake tmux that emits tmux's real mismatch message and exits non-zero,
	// fast (no hang — this is NOT the timeout path).
	script := "#!/bin/sh\n" +
		"echo 'protocol version mismatch (client 8, server 7)' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	restore := hasSessionProbeTimeout
	hasSessionProbeTimeout = 2 * time.Second
	t.Cleanup(func() { hasSessionProbeTimeout = restore })

	// The mismatch verdict is cached per socket; start from a clean slate so
	// this test's socket cannot inherit a verdict from a sibling test.
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	// Non-default socket skips the session cache; a unique name guarantees no
	// live pipe connection — so Exists() reaches the subprocess probe.
	s := &Session{Name: "mismatch-session", SocketName: "agent-deck-mismatch-test"}

	if !s.Exists() {
		t.Fatalf("Exists() returned false on a protocol-version-mismatch probe; " +
			"a mismatch proves the server is alive and must not be treated as a dead session")
	}
}

// TestSession_Exists_AuthoritativeAbsentStillReportsFalse guards the mismatch
// fix from over-broadening: a probe that COMPLETES with an ordinary non-zero
// exit (no mismatch marker) — the shape of tmux's "can't find session" — must
// still report the session as gone.
func TestSession_Exists_AuthoritativeAbsentStillReportsFalse(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"echo \"can't find session: mismatch-session\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	restore := hasSessionProbeTimeout
	hasSessionProbeTimeout = 2 * time.Second
	t.Cleanup(func() { hasSessionProbeTimeout = restore })

	// The mismatch verdict is cached per socket; start from a clean slate so
	// this test's socket cannot inherit a verdict from a sibling test.
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	s := &Session{Name: "gone-session", SocketName: "agent-deck-gone-test"}

	if s.Exists() {
		t.Fatalf("Exists() returned true for an authoritative absent probe; " +
			"only a protocol mismatch (server alive) may be treated as indeterminate")
	}
}

// TestSession_Exists_AbsentSessionNamedLikeMarkerReportsFalse is the adversarial
// regression: tmux echoes the target in "can't find session: <name>", so a
// session NAMED "protocol version mismatch" produces an absence reply that
// literally contains the mismatch marker. Absence is authoritative and must win
// — the classifier's bare substring match would otherwise keep this dead
// session alive.
func TestSession_Exists_AbsentSessionNamedLikeMarkerReportsFalse(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"echo \"can't find session: protocol version mismatch\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	restore := hasSessionProbeTimeout
	hasSessionProbeTimeout = 2 * time.Second
	t.Cleanup(func() { hasSessionProbeTimeout = restore })

	// The mismatch verdict is cached per socket; start from a clean slate so
	// this test's socket cannot inherit a verdict from a sibling test.
	resetSocketMismatchCacheForTest()
	t.Cleanup(resetSocketMismatchCacheForTest)

	s := &Session{Name: "protocol version mismatch", SocketName: "agent-deck-named-marker-test"}

	if s.Exists() {
		t.Fatalf("Exists() returned true for an absent session named like the mismatch marker; " +
			"an authoritative \"can't find session\" reply must win over the substring match")
	}
}
