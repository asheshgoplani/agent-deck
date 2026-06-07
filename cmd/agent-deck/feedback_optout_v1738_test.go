package main

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// isolateFeedbackHome points HOME at a tempdir so feedback state and user
// config writes stay out of the developer's real ~/.agent-deck. Callers MUST
// defer the returned cleanup to restore the session config cache.
func isolateFeedbackHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	isolateLegacyHome(t, tmp)
	// ClearUserConfigCache so the next LoadUserConfig reads from the new HOME.
	session.ClearUserConfigCache()
	t.Cleanup(func() { session.ClearUserConfigCache() })
	return tmp
}

// ──────────────────────────────────────────────────────────────────
// Test a — v1.7.38: declining at the disclosure step MUST set a
// persistent opt-out. Previously this just printed "Not posted." and
// re-prompted on every future run.
// ──────────────────────────────────────────────────────────────────
func TestV1738_CLI_DeclineAtDisclosure_SetsOptOut(t *testing.T) {
	isolateFeedbackHome(t)
	defer withStdin(t, pipeInput(t, "4", "nice tool", "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	if err := handleFeedbackWithSender([]string{}, "1.7.38", m.sender, os.Stdin, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if m.ghCalled {
		t.Error("gh must NOT be called when user declines at disclosure")
	}

	st, _ := feedback.LoadState()
	if st.FeedbackEnabled {
		t.Error("declining at disclosure must set FeedbackEnabled=false (persistent opt-out)")
	}

	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if !cfg.Feedback.Disabled {
		t.Error("declining at disclosure must set config.toml [feedback].disabled=true")
	}

	out := stdout.String()
	if !strings.Contains(strings.ToLower(out), "disabled") &&
		!strings.Contains(strings.ToLower(out), "never") &&
		!strings.Contains(strings.ToLower(out), "not post") {
		t.Errorf("output should inform user that feedback is now disabled / not posted:\n---\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// Test b — v1.7.38: when opted out, explicit `agent-deck feedback`
// shows a re-enable prompt BEFORE the rating prompt. Declining exits
// cleanly without touching state.
// ──────────────────────────────────────────────────────────────────
func TestV1738_CLI_ExplicitOnOptedOut_AsksReenable_DeclineExits(t *testing.T) {
	isolateFeedbackHome(t)

	// Pre-seed opt-out state.
	st := &feedback.State{FeedbackEnabled: false, MaxShows: 3}
	if err := feedback.SaveState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	cfg, _ := session.LoadUserConfig()
	cfg.Feedback.Disabled = true
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Decline the re-enable prompt (default-N: empty / n both decline).
	defer withStdin(t, pipeInput(t, "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	if err := handleFeedbackWithSender([]string{}, "1.7.38", m.sender, os.Stdin, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}

	out := stdout.String()
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "disabled") {
		t.Errorf("re-enable prompt should mention that feedback is disabled:\n---\n%s", out)
	}
	if !strings.Contains(lower, "enable") {
		t.Errorf("re-enable prompt should ask about enabling:\n---\n%s", out)
	}
	if strings.Contains(out, "Rating (1-5") {
		t.Error("declining re-enable must NOT continue to the rating prompt")
	}
	if m.ghCalled {
		t.Error("gh must NOT be called when user declines re-enable")
	}

	// State must remain opted-out after decline.
	st2, _ := feedback.LoadState()
	if st2.FeedbackEnabled {
		t.Error("declining re-enable must leave FeedbackEnabled=false")
	}
	cfg2, _ := session.LoadUserConfig()
	if !cfg2.Feedback.Disabled {
		t.Error("declining re-enable must leave config.Feedback.Disabled=true")
	}
}

// ──────────────────────────────────────────────────────────────────
// Test b.2 — accepting the re-enable prompt clears opt-out in BOTH
// places and continues into the normal rating flow.
// ──────────────────────────────────────────────────────────────────
func TestV1738_CLI_ExplicitOnOptedOut_AcceptReenable_ClearsBoth(t *testing.T) {
	isolateFeedbackHome(t)

	// Pre-seed opt-out.
	_ = feedback.SaveState(&feedback.State{FeedbackEnabled: false, MaxShows: 3})
	cfg, _ := session.LoadUserConfig()
	cfg.Feedback.Disabled = true
	_ = session.SaveUserConfig(cfg)

	// Accept re-enable ("y"), then quit the rating ("q").
	defer withStdin(t, pipeInput(t, "y", "q"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	if err := handleFeedbackWithSender([]string{}, "1.7.38", m.sender, os.Stdin, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}

	st2, _ := feedback.LoadState()
	if !st2.FeedbackEnabled {
		t.Error("accepting re-enable must set FeedbackEnabled=true")
	}
	cfg2, _ := session.LoadUserConfig()
	if cfg2.Feedback.Disabled {
		t.Error("accepting re-enable must clear config.Feedback.Disabled")
	}
	if !strings.Contains(stdout.String(), "Rating (1-5") {
		t.Errorf("accepting re-enable should continue to rating prompt:\n---\n%s", stdout.String())
	}
}

// ──────────────────────────────────────────────────────────────────
// Test e — persistence: after opt-out via disclosure decline, a fresh
// LoadState + LoadUserConfig (simulating a process restart) both report
// the opt-out. Uses an isolated HOME so the writes truly round-trip
// through the filesystem rather than an in-memory cache.
// ──────────────────────────────────────────────────────────────────
func TestV1738_OptOut_PersistsAcrossRestart(t *testing.T) {
	isolateFeedbackHome(t)

	defer withStdin(t, pipeInput(t, "4", "", "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	if err := handleFeedbackWithSender([]string{}, "1.7.38", m.sender, os.Stdin, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}

	// Simulate restart: drop the config cache so the next LoadUserConfig
	// reads fresh from disk instead of returning the in-memory snapshot.
	session.ClearUserConfigCache()

	st, _ := feedback.LoadState()
	if st.FeedbackEnabled {
		t.Error("state.json must persist FeedbackEnabled=false across restart")
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after 'restart': %v", err)
	}
	if !cfg.Feedback.Disabled {
		t.Error("config.toml must persist [feedback].disabled=true across restart")
	}
}
