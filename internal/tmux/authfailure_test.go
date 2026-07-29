package tmux

import "testing"

// TestIsAuthFailureContent_MatchesCredentialBanners asserts the shapes Claude
// Code actually renders on an auth failure are recognised.
func TestIsAuthFailureContent_MatchesCredentialBanners(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "401 with structured error payload",
			content: "some earlier output\nAPI Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"Invalid authentication credentials\"}}\n",
		},
		{
			name:    "please run /login instruction",
			content: "Please run /login · API Error: 401\n",
		},
		{
			name:    "expired oauth token",
			content: "API Error: 401 {\"type\":\"error\",\"error\":{\"message\":\"OAuth token has expired\"}}\n",
		},
		{
			name:    "invalid api key",
			content: "Invalid API key · Please run /login\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsAuthFailureContent("claude", tc.content) {
				t.Fatalf("expected credential-failure verdict for %q", tc.content)
			}
		})
	}
}

// TestIsAuthFailureContent_ExcludesRestartRecoverable is the load-bearing
// distinction of the whole auth-hold feature: a dropped socket shares the
// auth-401 SUBSTATE but is restart-recoverable, so it must NOT be swept into the
// hold — holding it would strand a session a restart would have fixed.
func TestIsAuthFailureContent_ExcludesRestartRecoverable(t *testing.T) {
	socketDrop := "API Error (Connection error.) · socket connection closed\n"
	if IsAuthFailureContent("claude", socketDrop) {
		t.Fatal("a dropped socket must not count as a credential failure: a restart fixes it")
	}
	// Sanity: the coarse error-banner detector DOES still match it, so status
	// detection is unchanged — only the auth attribution is narrower.
	if !hasClaudeErrorBanner(socketDrop) {
		t.Fatal("socket-drop banner must still register as an error banner")
	}
}

// TestIsAuthFailureContent_RespectsOverMatchGuards asserts the auth scan inherits
// every guard the error-banner scan has. A conductor reading a child's pane, or a
// human typing about a 401, must not arm a hold on the READING session.
func TestIsAuthFailureContent_RespectsOverMatchGuards(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "quoted tool result",
			content: "⎿  API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\"}}\n",
		},
		{
			name:    "user input line",
			content: "❯ why did the worker show API Error: 401 · Please run /login\n",
		},
		{
			name:    "box-drawing dialog content",
			content: "│ Please run /login\n",
		},
		{
			name:    "assistant prose without a structural banner marker",
			content: "⏺ The worker showed API Error: 401 and then exited\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsAuthFailureContent("claude", tc.content) {
				t.Fatalf("guarded line must not arm an auth hold: %q", tc.content)
			}
		})
	}
}

// TestIsAuthFailureContent_ClaudeOnly asserts other tools' renderings are never
// interpreted with Claude's heuristics.
func TestIsAuthFailureContent_ClaudeOnly(t *testing.T) {
	banner := "API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\"}}\n"
	for _, tool := range []string{"", "codex", "opencode", "shell", "gemini"} {
		if IsAuthFailureContent(tool, banner) {
			t.Fatalf("tool %q must not use Claude auth heuristics", tool)
		}
	}
	if !IsAuthFailureContent("CLAUDE", banner) {
		t.Fatal("tool name must be matched case-insensitively")
	}
}

// TestLastSampleAuthFailure_TracksLatestReadableSample is the precise semantic
// the death path depends on: the verdict describes the LAST sample that could
// read the pane, so a recovery clears it and a session that recovered then died
// of something else is never blamed on auth.
func TestLastSampleAuthFailure_TracksLatestReadableSample(t *testing.T) {
	s := &Session{}

	if _, ok := s.LastSampleAuthFailure(); ok {
		t.Fatal("a fresh session must have no auth-failure verdict")
	}

	s.mu.Lock()
	s.noteSampleAuthFailureLocked(true, "line one\nAPI Error: 401 · Please run /login")
	s.mu.Unlock()

	evidence, ok := s.LastSampleAuthFailure()
	if !ok {
		t.Fatal("expected the positive verdict to be recorded")
	}
	if evidence == "" {
		t.Fatal("expected the evidence snapshot to be retained")
	}

	// A later healthy sample must clear the verdict — otherwise a session that
	// recovered and much later died of an unrelated crash would be wrongly held.
	s.mu.Lock()
	s.noteSampleAuthFailureLocked(false, "✻ Crunched for 12s")
	s.mu.Unlock()

	if _, ok := s.LastSampleAuthFailure(); ok {
		t.Fatal("a healthy sample must clear the credential-failure verdict")
	}
}

// TestNoteSampleAuthFailure_KeepsEvidenceOnNegativeSample asserts a negative
// sample does not overwrite the evidence text. The verdict is what gates; the
// text is only ever read when the verdict is positive, and clobbering it would
// mean a positive verdict could arrive with unrelated content attached.
func TestNoteSampleAuthFailure_KeepsEvidenceOnNegativeSample(t *testing.T) {
	s := &Session{}
	s.mu.Lock()
	s.noteSampleAuthFailureLocked(true, "API Error: 401 · Please run /login")
	s.noteSampleAuthFailureLocked(false, "unrelated healthy output")
	kept := s.lastAuthFailureContent
	s.mu.Unlock()

	if kept != "API Error: 401 · Please run /login" {
		t.Fatalf("evidence must survive a negative sample, got %q", kept)
	}
}

// TestTailLines_BoundsEvidence keeps the retained snapshot small enough to hold
// per session and to write into a sidecar.
func TestTailLines_BoundsEvidence(t *testing.T) {
	content := ""
	for i := 0; i < 100; i++ {
		content += "line\n"
	}
	got := tailLines(content, 5)
	lines := 1
	for _, r := range got {
		if r == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Fatalf("expected 5 retained lines, got %d", lines)
	}
}
