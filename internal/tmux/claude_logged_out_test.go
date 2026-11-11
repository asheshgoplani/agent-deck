package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Claude Code 2.1.322 draws its own reply to a message sent while logged out
// ("/logout", or no credentials) as `  ⎿  Not logged in · Please run /login`
// directly under the submitted prompt. These tests pin that the shape is an
// auth failure (substate auth-401 + auth hold), and — more important, since an
// auth hold on a healthy session stops work — that the same text anywhere else
// is not.

const loggedOutLine = "  ⎿  Not logged in · Please run /login"

func loggedOutFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude_logged_out", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClaudeLoggedOut_RealPaneIsAuthFailure(t *testing.T) {
	for _, name := range []string{"not-logged-in.txt", "not-logged-in-crunched.txt"} {
		t.Run(name, func(t *testing.T) {
			pane := loggedOutFixture(t, name)
			if !IsAuthFailureContent("claude", pane) {
				t.Error("IsAuthFailureContent = false, want true")
			}
			if !hasClaudeErrorBanner(pane) {
				t.Error("hasClaudeErrorBanner = false, want true")
			}
			if got := NewPromptDetector("claude").ClassifySubstate(pane); got != SubstateAuth401 {
				t.Errorf("substate = %q, want %q", got, SubstateAuth401)
			}
		})
	}
}

// The fixture with "Crunched for 0s" must not fall through to model-unavailable.
func TestClaudeLoggedOut_CrunchedIsNotModelUnavailable(t *testing.T) {
	pane := loggedOutFixture(t, "not-logged-in-crunched.txt")
	if !strings.Contains(pane, "Crunched for 0s") {
		t.Fatal("fixture lost its Crunched for 0s line")
	}
	if got := NewPromptDetector("claude").ClassifySubstate(pane); got == SubstateModelUnavailable {
		t.Fatalf("substate = %q, want auth-401", got)
	}
}

// The existing "Login expired" and API-401 routes stay auth-401 with a hold.
func TestClaudeLoggedOut_ExistingAuthRoutesPinned(t *testing.T) {
	for _, name := range []string{"login-expired.txt", "api-401.txt"} {
		t.Run(name, func(t *testing.T) {
			pane := loggedOutFixture(t, name)
			if !IsAuthFailureContent("claude", pane) {
				t.Error("IsAuthFailureContent = false, want true")
			}
			if got := NewPromptDetector("claude").ClassifySubstate(pane); got != SubstateAuth401 {
				t.Errorf("substate = %q, want %q", got, SubstateAuth401)
			}
		})
	}
}

func TestClaudeLoggedOut_NoFalsePositive(t *testing.T) {
	footer := "\n✻ Sautéed for 0s · done 7:45 PM\n\n" + strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ⏵⏵ auto mode on\n"
	cases := map[string]string{
		"quoted under a tool line": "❯ check the child\n\n● Bash(agent-deck session output login-b)\n" + loggedOutLine + "\n\n● It is logged out.\n" + footer,
		"old history above a later turn": "❯ first\n" + loggedOutLine + "\n\n✻ Sautéed for 0s · done 7:45 PM\n\n" +
			"❯ second\n\n● All fine, 3 files changed.\n" + footer,
		"typed into the composer":          "● ready\n\n" + strings.Repeat("─", 40) + "\n❯ Not logged in · Please run /login\n" + strings.Repeat("─", 40) + "\n  ⏵⏵ auto mode on\n",
		"submitted prompt quoting it":      "❯ Not logged in · Please run /login\n\n● That is what Claude prints when logged out.\n" + footer,
		"indented code block in a reply":   "❯ show me a transcript\n\n● Here is one:\n\n  ❯ count to 3\n  " + strings.TrimLeft(loggedOutLine, " ") + "\n\n● That is the logged-out shape.\n" + footer,
		"pasted log in a reply":            "❯ what happened\n\n● The log said:\n\n    ❯ count to 3\n    ⎿  Not logged in · Please run /login\n\n● So the user logged out.\n" + footer,
		"different text under the prompt":  "❯ count to 3\n  ⎿  Not logged in elsewhere, ignore\n" + footer,
		"under prompt but not at turn end": "❯ count to 3\n" + loggedOutLine + "\n\n● Sure: 1, 2, 3.\n" + footer,
	}
	for name, pane := range cases {
		t.Run(name, func(t *testing.T) {
			if IsAuthFailureContent("claude", pane) {
				t.Error("IsAuthFailureContent = true, want false")
			}
			if hasClaudeErrorBanner(pane) {
				t.Error("hasClaudeErrorBanner = true, want false")
			}
			if got := NewPromptDetector("claude").ClassifySubstate(pane); got == SubstateAuth401 {
				t.Errorf("substate = auth-401, want not auth-401")
			}
		})
	}
}

// Only Claude's arm changes: no other built-in tool treats the line as auth.
func TestClaudeLoggedOut_OtherToolsUntouched(t *testing.T) {
	pane := loggedOutFixture(t, "not-logged-in.txt")
	for _, tool := range []string{"codex", "gemini", "opencode", "deepseek", "shell", ""} {
		if IsAuthFailureContent(tool, pane) {
			t.Errorf("IsAuthFailureContent(%q) = true, want false", tool)
		}
	}
}
