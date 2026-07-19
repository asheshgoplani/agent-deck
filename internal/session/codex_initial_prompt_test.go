package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// A large initial prompt cannot be typed into a live Codex TUI: Agent Deck sends it
// as one literal `tmux send-keys -l` burst followed immediately by Enter, and Codex
// reads a large fast burst as a paste, swallowing the Enter into it. The prompt then
// sits unsubmitted in the composer and the agent never starts. Codex accepts the
// prompt as its own positional argument (`codex [OPTIONS] [PROMPT]`), so deliver it
// there and type nothing.

func codexInstance(tool, command string) *Instance {
	return &Instance{ID: "i1", Title: "t", Tool: tool, Command: command}
}

func TestBuildCodexCommandWithPromptEmbedsPromptAsArgument(t *testing.T) {
	i := codexInstance("codex", "codex")
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", "do the thing")
	if !embedded {
		t.Fatalf("prompt should be embedded on the plain fresh-start path; got %q", cmd)
	}
	if !strings.HasSuffix(cmd, " 'do the thing'") {
		t.Fatalf("prompt must be the last, shell-quoted argument; got %q", cmd)
	}
}

func TestBuildCodexCommandWithPromptQuotesMetacharacters(t *testing.T) {
	i := codexInstance("codex", "codex")
	prompt := "IT'S \"quoted\" `tick` $VAR; rm -rf / && echo pwned"
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", prompt)
	if !embedded {
		t.Fatal("expected prompt to be embedded")
	}
	// The whole prompt must be exactly one shell-quoted operand, so nothing in it
	// can be interpreted by the shell that runs the command.
	if !strings.HasSuffix(cmd, shellescape.Quote(prompt)) {
		t.Fatalf("prompt is not a single shell-quoted operand; got %q", cmd)
	}
	if !strings.Contains(cmd, `'"'"'`) {
		t.Fatalf("apostrophes must be shell-escaped; got %q", cmd)
	}
}

func TestBuildCodexCommandWithPromptEmptyPromptIsNotEmbedded(t *testing.T) {
	i := codexInstance("codex", "codex")
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", "   ")
	if embedded {
		t.Fatalf("an empty prompt must not be embedded; got %q", cmd)
	}
}

func TestBuildCodexCommandWithPromptCustomCommandFallsBackToTyping(t *testing.T) {
	// A user-supplied wrapper is passed through verbatim and need not accept a
	// positional prompt, so the caller must keep the existing typing path.
	i := codexInstance("codex", "my-wrapper --flag")
	cmd, embedded := i.buildCodexCommandWithPrompt("my-wrapper --flag", "do the thing")
	if embedded {
		t.Fatalf("custom commands must not get a positional prompt; got %q", cmd)
	}
}

func TestBuildCodexCommandWithPromptResumeFallsBackToTyping(t *testing.T) {
	// buildCodexCommand drops a session id with no rollout on disk (#756), which is a
	// fresh start; a resumable session needs its rollout file to exist.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	sid := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "07", "14")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-07-14T00-00-00-"+sid+".jsonl"),
		[]byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	i := codexInstance("codex", "codex")
	i.CodexSessionID = sid
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", "do the thing")
	if embedded {
		t.Fatalf("resume path must not get a positional prompt; got %q", cmd)
	}
	if !strings.Contains(cmd, "resume ") {
		t.Fatalf("expected the resume command; got %q", cmd)
	}
}
