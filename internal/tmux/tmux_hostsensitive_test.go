//go:build hostsensitive

// Host-sensitive tmux tests. Built and run only when the `hostsensitive`
// build tag is supplied (e.g. nightly job: `go test -tags hostsensitive`).
// See issue #969 for the categorization rationale.

package tmux

import "testing"

// TestSession_SetAndGetEnvironment exercises tmux `set-environment` /
// `show-environment` round-tripping. It depends on a live external tmux
// server (skipIfNoTmuxServer) AND on the host's tmux build correctly
// honoring per-session environment without leaking process-environment
// fallbacks — observed flaky across some Linux + macOS hosts when the
// global session-environment has lingering state. Gate behind the
// hostsensitive tag so pre-push / default CI stays green.
func TestSession_SetAndGetEnvironment(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Create a test session
	sess := NewSession("env-test", "/tmp")

	// Start the session (required for environment to work)
	err := sess.Start("")
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	// Test setting environment
	err = sess.SetEnvironment("TEST_VAR", "test_value_123")
	if err != nil {
		t.Fatalf("SetEnvironment failed: %v", err)
	}

	// Test getting environment
	value, err := sess.GetEnvironment("TEST_VAR")
	if err != nil {
		t.Fatalf("GetEnvironment failed: %v", err)
	}

	if value != "test_value_123" {
		t.Errorf("GetEnvironment = %q, want %q", value, "test_value_123")
	}
}
