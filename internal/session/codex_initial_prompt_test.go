package session

import (
	"os"
	"os/exec"
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

// tmux carries a `new-session` command line to its server as a single imsg and
// refuses anything over MAX_IMSGSIZE with "command too long" — the launch fails
// outright, leaving a dead session to archive (orchestrate run
// 2026-08-19-ob-live-e2e, a 15k reviewer prompt). Above the budget the prompt
// moves to a file and the command reads it back at exec time, where the only
// ceiling is ARG_MAX (1 MB), not tmux's 16 KB.

// codexPromptFileInstance gives each test its own throwaway repository. The
// spill path is resolved against the project's Git worktree root, so a bare
// t.TempDir() would resolve into whatever repository TMPDIR happens to sit in
// and let sibling tests collide on one path.
func codexPromptFileInstance(t *testing.T, id string) *Instance {
	t.Helper()
	project := t.TempDir()
	if out, err := exec.Command("git", "-C", project, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	i := codexInstance("codex", "codex")
	i.ID = id
	i.ProjectPath = project
	return i
}

func TestBuildCodexCommandWithPromptOversizePromptMovesToFile(t *testing.T) {
	i := codexPromptFileInstance(t, "codex-oversize")
	prompt := strings.Repeat("a", tmuxCommandByteBudget+1)

	cmd, embedded := i.buildCodexCommandWithPrompt("codex", prompt)
	if !embedded {
		t.Fatalf("an oversize prompt must still be delivered on the command line; got %q", cmd)
	}
	if strings.Contains(cmd, prompt) {
		t.Fatal("the prompt body must not be inlined into the tmux command")
	}
	if len(cmd) > tmuxCommandByteBudget {
		t.Fatalf("command is %d bytes, over the %d budget", len(cmd), tmuxCommandByteBudget)
	}
	path := i.codexInitialPromptPath()
	if !strings.Contains(cmd, shellescape.Quote(path)) {
		t.Fatalf("command must read the prompt back from %q; got %q", path, cmd)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prompt file not written: %v", err)
	}
	if string(body) != prompt {
		t.Fatalf("prompt file holds %d bytes, want %d", len(body), len(prompt))
	}
}

func TestBuildCodexCommandWithPromptUnderBudgetStaysInline(t *testing.T) {
	i := codexPromptFileInstance(t, "codex-under-budget")
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", "do the thing")
	if !embedded {
		t.Fatalf("expected the prompt to be embedded; got %q", cmd)
	}
	if !strings.HasSuffix(cmd, " 'do the thing'") {
		t.Fatalf("an under-budget prompt must stay inline, byte-for-byte as before; got %q", cmd)
	}
	if _, err := os.Stat(i.codexInitialPromptPath()); !os.IsNotExist(err) {
		t.Fatal("no prompt file may be written for an under-budget prompt")
	}
}

func TestBuildCodexCommandWithPromptOversizeSSHFallsBackToTyping(t *testing.T) {
	// The prompt file is written host-side; an SSH pane runs the command on
	// another machine where that path does not exist. Typing is the only
	// transport left.
	i := codexPromptFileInstance(t, "codex-ssh")
	i.SSHHost = "somehost"
	cmd, embedded := i.buildCodexCommandWithPrompt("codex", strings.Repeat("a", tmuxCommandByteBudget+1))
	if embedded {
		t.Fatalf("an SSH session must not get a host-side prompt file; got %q", cmd)
	}
}
