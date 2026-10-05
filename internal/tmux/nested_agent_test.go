package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A copy of the test binary named "opencode" is the live nested-agent fixture.
// GNU sleep is a multicall binary and refuses to run under another name, so
// the process has to be a real executable whose comm is the agent. init exits
// before the test harness when that is how we were invoked.
func init() {
	if filepath.Base(os.Args[0]) == "opencode" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

func TestDetectNestedAgent_OutermostAncestorNotDescendant(t *testing.T) {
	// shell → nvim → opencode, and opencode has spawned a codex child for a
	// tool call. The outermost agent is opencode, not the descendant.
	match, status := detectNestedAgent(10, []processSample{
		{PID: 10, PPID: 1, Comm: "bash"},
		{PID: 11, PPID: 10, Comm: "nvim"},
		{PID: 12, PPID: 11, Comm: "bash"},
		{PID: 13, PPID: 12, Comm: "/usr/local/bin/opencode", StartID: "13"},
		{PID: 14, PPID: 13, Comm: "codex"},
	})
	if status != agentTreeMatch || match.Tool != "opencode" || match.PID != 13 {
		t.Fatalf("match = %+v status=%v, want opencode pid 13", match, status)
	}
}

func TestDetectNestedAgent_ClaudeAncestorBeatsChildCodex(t *testing.T) {
	match, status := detectNestedAgent(1, []processSample{
		{PID: 1, PPID: 0, Comm: "zsh"},
		{PID: 2, PPID: 1, Comm: "claude"},
		{PID: 3, PPID: 2, Comm: "bash"},
		{PID: 4, PPID: 3, Comm: "codex"},
	})
	if status != agentTreeMatch || match.Tool != "claude" || match.PID != 2 {
		t.Fatalf("match = %+v status=%v, want claude pid 2", match, status)
	}
}

func TestDetectNestedAgent_AmbiguousSiblingsFailClosed(t *testing.T) {
	// Two Neovim terminals: neither agent is an ancestor of the other.
	_, status := detectNestedAgent(1, []processSample{
		{PID: 1, PPID: 0, Comm: "bash"},
		{PID: 2, PPID: 1, Comm: "nvim"},
		{PID: 3, PPID: 2, Comm: "claude"},
		{PID: 4, PPID: 2, Comm: "gemini"},
	})
	if status != agentTreeAmbiguous {
		t.Fatalf("status = %v, want ambiguous", status)
	}
}

func TestDetectNestedAgent_MissingPaneAndZombieFailClosed(t *testing.T) {
	if _, status := detectNestedAgent(99, []processSample{{PID: 1, PPID: 0, Comm: "opencode"}}); status != agentTreeUnavailable {
		t.Fatalf("status = %v, want unavailable when the pane PID is missing", status)
	}
	if _, status := detectNestedAgent(1, []processSample{
		{PID: 1, PPID: 0, Comm: "bash"},
		{PID: 2, PPID: 1, Comm: "opencode", Zombie: true},
	}); status != agentTreeNone {
		t.Fatalf("status = %v, want none for a zombie agent", status)
	}
}

func TestClassifyProcess_ExactExecutableAndInterpreterEntrypoint(t *testing.T) {
	tests := []struct {
		name string
		proc processSample
		want string
		ok   bool
	}{
		{"exact claude", processSample{Comm: "/usr/bin/claude"}, "claude", true},
		{"cursor agent binary", processSample{Comm: "agent"}, "cursor", true},
		{"substring is not an executable", processSample{Comm: "claude-deck"}, "", false},
		{"node opencode script", processSample{
			Comm: "node",
			Argv: []string{"node", "/usr/lib/node_modules/opencode/bin/opencode.js"},
		}, "opencode", true},
		{"node script in a claude directory", processSample{
			Comm: "node",
			Argv: []string{"node", "/home/user/claude/index.js", "--model", "gemini"},
		}, "", false},
		{"inline eval is not an entrypoint", processSample{
			Comm: "node",
			Argv: []string{"node", "-e", "console.log('claude')"},
		}, "", false},
		{"python -m opencode", processSample{
			Comm: "python3",
			Argv: []string{"python3", "-m", "opencode"},
		}, "opencode", true},
		{"python -c is prose", processSample{
			Comm: "python3",
			Argv: []string{"python3", "-c", "import claude"},
		}, "", false},
		{"npx omp package", processSample{
			Comm: "npx",
			Argv: []string{"npx", "--yes", "@oh-my-pi/pi-coding-agent"},
		}, "omp", true},
		{"env wrapper", processSample{
			Comm: "env",
			Argv: []string{"env", "FOO=bar", "opencode"},
		}, "opencode", true},
		{"node without argv fails closed", processSample{Comm: "node"}, "", false},
		{"agent.js is not cursor", processSample{
			Comm: "node",
			Argv: []string{"node", "agent.js"},
		}, "", false},
		{"dsh basename", processSample{Comm: "dsh"}, "deepseek", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyProcess(tt.proc)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("classifyProcess(%q) = %q, %v; want %q, %v", tt.proc.Comm, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDetectTool_NestedAgentIgnoresScreenText(t *testing.T) {
	sess := NewSession("nested-agent-screen", "/tmp")
	sess.Command = "shell"
	sess.cacheContent = "Welcome to Claude Code! Gemini API key OpenAI Codex"
	sess.cacheTime = time.Now()
	seedPaneCommand(t, sess.Name, "nvim")
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "zsh"},
		{PID: 11, PPID: 10, Comm: "nvim"},
		{PID: 12, PPID: 11, Comm: "node", Argv: []string{"node", "/opt/opencode/bin/opencode"}, StartID: "12"},
	})

	if got := sess.DetectTool(); got != "opencode" {
		t.Fatalf("DetectTool() = %q, want opencode from the process tree, not pane text", got)
	}
	if got := sess.ForceDetectTool(); got != "opencode" {
		t.Fatalf("ForceDetectTool() = %q, want opencode", got)
	}
}

func TestDetectTool_AmbiguousSiblingsDoNotUseScreenText(t *testing.T) {
	sess := NewSession("nested-agent-ambiguous", "/tmp")
	sess.Command = "shell"
	sess.cacheContent = "Welcome to Claude Code!"
	sess.cacheTime = time.Now()
	seedPaneCommand(t, sess.Name, "nvim")
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "bash"},
		{PID: 11, PPID: 10, Comm: "nvim"},
		{PID: 12, PPID: 11, Comm: "claude"},
		{PID: 13, PPID: 11, Comm: "gemini"},
	})

	if got := sess.DetectTool(); got != "shell" {
		t.Fatalf("DetectTool() = %q, want shell for ambiguous sibling agents", got)
	}
}

func TestDetectTool_EmptyTreeStillPromotesForegroundCommand(t *testing.T) {
	sess := NewSession("nested-agent-empty-tree", "/tmp")
	sess.Command = "shell"
	sess.cacheContent = "Welcome to Claude Code!"
	sess.cacheTime = time.Now()
	seedPaneCommand(t, sess.Name, "claude")
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "bash"},
	})
	if got := sess.DetectTool(); got != "claude" {
		t.Fatalf("DetectTool() = %q, want foreground claude when the tree has no agent", got)
	}
}

func TestDetectTool_ConfiguredCommandBeatsTree(t *testing.T) {
	sess := NewSession("nested-agent-configured", "/tmp")
	sess.Command = "claude"
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "nvim"},
		{PID: 11, PPID: 10, Comm: "opencode"},
	})
	if got := sess.DetectTool(); got != "claude" {
		t.Fatalf("DetectTool() = %q, want configured launch identity", got)
	}
}

func TestDetectTool_DropsBoundAgentWhenProcessDies(t *testing.T) {
	sess := NewSession("nested-agent-lifetime", "/tmp")
	sess.Command = "shell"
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "bash"},
		{PID: 11, PPID: 10, Comm: "opencode", StartID: "100"},
	})
	if got := sess.DetectTool(); got != "opencode" {
		t.Fatalf("DetectTool() = %q, want opencode", got)
	}

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	processIdentityOf = func(context.Context, int) (string, error) {
		return "", errors.New("no such process")
	}
	sess.SetAgentTreeForTest(t, 10, []PaneProcess{
		{PID: 10, PPID: 1, Comm: "bash"},
	})

	if got := sess.DetectTool(); got != "shell" {
		t.Fatalf("DetectTool() = %q, want shell after the bound agent exited", got)
	}
}

func TestDetectToolDoesNotPromoteLiveNestedAgentFromScreenText(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	self, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	agentBin := filepath.Join(dir, "opencode")
	require.NoError(t, copyExecutable(self, agentBin))

	sess := NewSession("nested-agent-live", t.TempDir())
	sess.Command = "shell"
	sess.SocketName = fmt.Sprintf("agentdeck-nested-%d", time.Now().UnixNano())
	script := fmt.Sprintf("printf 'Welcome to Claude Code! Gemini Codex\\n'; %s 60 & wait", shellQuote(agentBin))
	output, err := exec.Command("tmux", "-L", sess.SocketName, "-f", "/dev/null",
		"new-session", "-d", "-s", sess.Name, script).CombinedOutput()
	if err != nil {
		t.Fatalf("create isolated tmux server: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", sess.SocketName, "kill-server").Run()
	})

	deadline := time.Now().Add(5 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		output, err = exec.Command("tmux", "-L", sess.SocketName,
			"capture-pane", "-p", "-t", sess.Name).CombinedOutput()
		if err != nil {
			t.Fatalf("capture isolated pane: %v: %s", err, output)
		}
		content = string(output)
		if strings.Contains(content, "Welcome to Claude Code!") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Contains(t, content, "Welcome to Claude Code!")

	deadline = time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		ExpireToolDetectionForTest(sess)
		got = sess.DetectTool()
		if got == "opencode" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("live pane output: %q; detected tool: %s", strings.TrimSpace(content), got)
	if got != "opencode" {
		t.Fatalf("DetectTool() = %q, want opencode from the pane process tree, not screen text", got)
	}
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
