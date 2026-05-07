package integration

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// DETECT-01: Pattern detection tests per tool
// =============================================================================

// TestDetection_ClaudeBusy verifies that PromptDetector("claude").HasPrompt returns
// false when Claude is busy (spinner, ctrl+c, whimsical words with timing).
func TestDetection_ClaudeBusy(t *testing.T) {
	detector := tmux.NewPromptDetector("claude")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "spinner with whimsical word",
			content: "Some output\n\u2722 Clauding\u2026 (25s \u00b7 \u2193 749 tokens)\n",
		},
		{
			name:    "ctrl+c to interrupt",
			content: "Working on request\nctrl+c to interrupt\n",
		},
		{
			name:    "whimsical ellipsis and tokens",
			content: "Output\n\u2026 tokens in progress\n\u2722 Pondering\u2026 (10s \u00b7 \u2193 200 tokens)\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, detector.HasPrompt(tc.content),
				"HasPrompt should return false for busy content: %s", tc.name)
		})
	}
}

// TestDetection_ClaudeWaiting verifies that PromptDetector("claude").HasPrompt returns
// true when Claude is waiting for user input (prompt, permission dialog, trust prompt).
func TestDetection_ClaudeWaiting(t *testing.T) {
	detector := tmux.NewPromptDetector("claude")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "input prompt skip-permissions mode",
			content: "Task completed.\n\u276f \n",
		},
		{
			name:    "permission dialog",
			content: "\u2502 Do you want to run this command?\n\u276f Yes, allow once\n",
		},
		{
			name:    "trust prompt",
			content: "Do you trust the files in this folder?\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, detector.HasPrompt(tc.content),
				"HasPrompt should return true for waiting content: %s", tc.name)
		})
	}
}

// TestDetection_GeminiBusy verifies that PromptDetector("gemini").HasPrompt returns
// false when Gemini shows busy indicators.
func TestDetection_GeminiBusy(t *testing.T) {
	detector := tmux.NewPromptDetector("gemini")

	// Note: Gemini's HasPrompt checks for prompt patterns but doesn't have
	// explicit busy indicators in the current detector implementation.
	// The "esc to cancel" text is a busy indicator from DefaultRawPatterns,
	// but the PromptDetector's hasGeminiPrompt checks last 10 non-blank lines
	// for prompt patterns. If none match, HasPrompt returns false.
	content := "Processing your request\nesc to cancel\n"
	assert.False(t, detector.HasPrompt(content),
		"HasPrompt should return false for Gemini busy content")
}

// TestDetection_GeminiWaiting verifies that PromptDetector("gemini").HasPrompt returns
// true when Gemini shows prompt indicators.
func TestDetection_GeminiWaiting(t *testing.T) {
	detector := tmux.NewPromptDetector("gemini")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "gemini prompt",
			content: "Previous output\ngemini>\n",
		},
		{
			name:    "type your message",
			content: "Welcome to Gemini CLI\nType your message\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, detector.HasPrompt(tc.content),
				"HasPrompt should return true for Gemini waiting content: %s", tc.name)
		})
	}
}

// TestDetection_OpenCodeBusy verifies that PromptDetector("opencode").HasPrompt returns
// false when OpenCode shows busy indicators.
func TestDetection_OpenCodeBusy(t *testing.T) {
	detector := tmux.NewPromptDetector("opencode")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "pulse spinner char",
			content: "Processing\n\u2588 Working on your request\n",
		},
		{
			name:    "esc interrupt",
			content: "Running task\nesc interrupt\n",
		},
		{
			name:    "thinking text",
			content: "OpenCode\nThinking...\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, detector.HasPrompt(tc.content),
				"HasPrompt should return false for OpenCode busy content: %s", tc.name)
		})
	}
}

// TestDetection_OpenCodeWaiting verifies that PromptDetector("opencode").HasPrompt returns
// true when OpenCode shows prompt indicators.
func TestDetection_OpenCodeWaiting(t *testing.T) {
	detector := tmux.NewPromptDetector("opencode")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "ask anything",
			content: "Welcome to OpenCode\nAsk anything\n",
		},
		{
			name:    "press enter to send",
			content: "Output here\npress enter to send\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, detector.HasPrompt(tc.content),
				"HasPrompt should return true for OpenCode waiting content: %s", tc.name)
		})
	}
}

// TestDetection_CodexBusy verifies that PromptDetector("codex").HasPrompt returns
// false when Codex shows busy indicators.
func TestDetection_CodexBusy(t *testing.T) {
	detector := tmux.NewPromptDetector("codex")

	content := "Running your task\nesc to interrupt\n"
	assert.False(t, detector.HasPrompt(content),
		"HasPrompt should return false for Codex busy content")
}

// TestDetection_CodexWaiting verifies that PromptDetector("codex").HasPrompt returns
// true when Codex shows prompt indicators.
func TestDetection_CodexWaiting(t *testing.T) {
	detector := tmux.NewPromptDetector("codex")

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "codex prompt",
			content: "Ready\ncodex>\n",
		},
		{
			name:    "continue prompt",
			content: "Task paused\nContinue?\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, detector.HasPrompt(tc.content),
				"HasPrompt should return true for Codex waiting content: %s", tc.name)
		})
	}
}

// =============================================================================
// DETECT-02: DefaultRawPatterns, CompilePatterns, ToolConfig
// =============================================================================

// TestDetection_DefaultPatternsExist verifies that DefaultRawPatterns returns non-nil
// for all four supported tools and nil for unknown tools.
func TestDetection_DefaultPatternsExist(t *testing.T) {
	knownTools := []string{"claude", "gemini", "opencode", "codex"}
	for _, tool := range knownTools {
		t.Run(tool, func(t *testing.T) {
			raw := tmux.DefaultRawPatterns(tool)
			assert.NotNil(t, raw, "DefaultRawPatterns(%q) should return non-nil", tool)
		})
	}

	t.Run("unknown tool returns nil", func(t *testing.T) {
		raw := tmux.DefaultRawPatterns("unknown-tool-xyz")
		assert.Nil(t, raw, "DefaultRawPatterns for unknown tool should return nil")
	})
}

// TestDetection_CompilePatterns verifies that CompilePatterns on Claude's
// DefaultRawPatterns produces valid ResolvedPatterns with populated fields.
func TestDetection_CompilePatterns(t *testing.T) {
	raw := tmux.DefaultRawPatterns("claude")
	require.NotNil(t, raw, "DefaultRawPatterns for claude must exist")

	resolved, err := tmux.CompilePatterns(raw)
	require.NoError(t, err, "CompilePatterns should not error")
	require.NotNil(t, resolved, "ResolvedPatterns should not be nil")

	// Claude patterns include "re:" prefixed regex patterns, so BusyRegexps should be populated
	assert.NotEmpty(t, resolved.BusyRegexps, "BusyRegexps should contain compiled regex patterns")

	// SpinnerChars should be copied from raw
	assert.NotEmpty(t, resolved.SpinnerChars, "SpinnerChars should be populated")
	assert.Equal(t, len(raw.SpinnerChars), len(resolved.SpinnerChars),
		"SpinnerChars count should match raw patterns")

	// Claude has WhimsicalWords + SpinnerChars, so combo patterns should be built
	assert.NotNil(t, resolved.ThinkingPattern, "ThinkingPattern should be compiled")
	assert.NotNil(t, resolved.ThinkingPatternEllipsis, "ThinkingPatternEllipsis should be compiled")
	assert.NotNil(t, resolved.SpinnerActivePattern, "SpinnerActivePattern should be compiled")
}

// TestDetection_ToolConfig verifies that NewInstanceWithTool correctly sets the
// Tool field for each supported tool type.
func TestDetection_ToolConfig(t *testing.T) {
	tools := []string{"claude", "gemini", "opencode", "codex", "shell", "cursor"}

	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			inst := session.NewInstanceWithTool("test-"+tool, "/tmp", tool)
			assert.Equal(t, tool, inst.Tool,
				"NewInstanceWithTool(%q) should set Tool field correctly", tool)
		})
	}
}

// =============================================================================
// DETECT-03: Real tmux status transition cycle tests
// =============================================================================

// TestDetection_StatusCycle_ShellSession verifies that a shell session transitions
// from StatusStarting to StatusIdle after its command completes and the shell
// prompt appears. This exercises the full UpdateStatus -> GetStatus -> CapturePane
// -> detection pipeline through a real tmux session.
//
// Per the codebase: shell sessions at a prompt map tmux "waiting" to StatusIdle
// (not StatusWaiting), because tool="shell" has a special mapping in UpdateStatus().
func TestDetection_StatusCycle_ShellSession(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("status-cycle", "/tmp")
	inst.Command = "echo status-marker && sleep 1"
	require.NoError(t, inst.Start())

	// Immediately after Start(), status should be StatusStarting.
	assert.Equal(t, session.StatusStarting, inst.Status,
		"status should be starting immediately after Start()")

	// Wait for the tmux session to be created.
	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"session to exist in tmux",
		func() bool { return inst.Exists() })

	// Wait for the command to produce its output marker.
	WaitForPaneContent(t, inst, "status-marker", 5*time.Second)

	// Wait for the command to complete (sleep 1 exits, shell prompt appears).
	// After command completion, the shell prompt ($ or %) becomes visible.
	// Per Pitfall 3: wait at least 500ms before UpdateStatus for stable results.
	time.Sleep(2 * time.Second)

	// Poll UpdateStatus until the status converges.
	// Per Pitfall 2: shell sessions at a prompt show StatusIdle, not StatusWaiting.
	WaitForCondition(t, 10*time.Second, 500*time.Millisecond,
		"status to converge to idle",
		func() bool {
			_ = inst.UpdateStatus()
			return inst.GetStatusThreadSafe() == session.StatusIdle
		})

	assert.Equal(t, session.StatusIdle, inst.GetStatusThreadSafe(),
		"shell session at prompt should be StatusIdle")
}

// TestDetection_StatusCycle_CommandRunning verifies that a shell session running
// a long command (sleep 30) converges to a non-error status after the grace period.
// This tests that UpdateStatus correctly processes real tmux pane content for
// sessions where the command is still executing.
//
// Note: Shell sessions without explicit busy indicators (spinners, "ctrl+c to
// interrupt") do not produce StatusRunning. The tmux pane still contains the
// shell prompt line (e.g., "$ sleep 30") which triggers prompt detection,
// resulting in tmux returning "waiting" mapped to StatusIdle for tool="shell".
func TestDetection_StatusCycle_CommandRunning(t *testing.T) {
	h := NewTmuxHarness(t)

	inst := h.CreateSession("status-running", "/tmp")
	inst.Command = "sleep 30"
	require.NoError(t, inst.Start())

	// Wait for the tmux session to exist.
	WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
		"session to exist in tmux",
		func() bool { return inst.Exists() })

	// Wait for the grace period to pass (1.5s per Pitfall 7).
	time.Sleep(2 * time.Second)

	// Call UpdateStatus repeatedly to let the detection pipeline stabilize.
	// The session should reach a non-error, non-starting status.
	WaitForCondition(t, 10*time.Second, 500*time.Millisecond,
		"status to be non-error and non-starting",
		func() bool {
			_ = inst.UpdateStatus()
			s := inst.GetStatusThreadSafe()
			return s != session.StatusError && s != session.StatusStarting
		})

	status := inst.GetStatusThreadSafe()
	assert.NotEqual(t, session.StatusError, status,
		"running session should not be in error state")
	assert.NotEqual(t, session.StatusStarting, status,
		"running session should have progressed past starting")

	// Verify the tmux session is still alive (sleep 30 hasn't exited).
	assert.True(t, inst.Exists(), "session should still exist while sleep 30 runs")
}
