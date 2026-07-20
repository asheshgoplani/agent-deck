package session

import (
	"os"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

func TestBuildOmpCommand_UsesInstanceScopedSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	inst := &Instance{ID: "test-instance-id", Tool: "omp"}
	got := inst.buildOmpCommand("omp")

	wantSessionDir := "${HOME}/.omp/agent-deck/test-instance-id"
	for _, want := range []string{
		"session_dir=" + wantSessionDir,
		"mkdir -p \"$session_dir\"",
		"AGENTDECK_INSTANCE_ID=test-instance-id",
		"omp --continue --session-dir \"$session_dir\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildOmpCommand output missing %q\ngot: %s", want, got)
		}
	}
}

func TestBuildOmpCommand_QuotesInstanceIDPathComponent(t *testing.T) {
	inst := &Instance{ID: "test instance'id", Tool: "omp"}
	got := inst.buildOmpCommand("omp")

	wantSessionDir := `${HOME}/.omp/agent-deck/` + shellescape.Quote(inst.ID)
	if !strings.Contains(got, "session_dir="+wantSessionDir) {
		t.Errorf("buildOmpCommand() should quote instance ID path component %q, got %q", wantSessionDir, got)
	}
}

func TestBuildOmpCommand_WrongTool(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	got := inst.buildOmpCommand("some-command")
	if got != "some-command" {
		t.Errorf("buildOmpCommand with wrong tool = %q, want %q", got, "some-command")
	}
}

func TestBuildOmpCommand_DefaultsBinary(t *testing.T) {
	inst := &Instance{ID: "tid", Tool: "omp"}
	got := inst.buildOmpCommand("")
	if !strings.Contains(got, " omp --continue") {
		t.Errorf("empty command must default to omp binary, got %q", got)
	}
}

func TestResolveDynamicToolPreservesOmp(t *testing.T) {
	// omp drives other agent CLIs (codex, claude) as subprocesses; child
	// detection must never rewrite its identity.
	for _, detected := range []string{"codex", "claude", "gemini", "opencode", "shell"} {
		if got := resolveDynamicTool("omp", detected, false); got != "omp" {
			t.Errorf("resolveDynamicTool(omp, %q) = %q, want omp", detected, got)
		}
	}
}

func TestResolveDynamicToolUpstreamBehaviorUnchanged(t *testing.T) {
	cases := []struct {
		current, detected string
		preserveCustom    bool
		want              string
	}{
		{"shell", "codex", false, "codex"},
		{"claude", "codex", false, "codex"},
		{"codex", "shell", false, "shell"},
		{"pi", "shell", false, "pi"},
		{"my-codex", "codex", true, "my-codex"},
		{"", "claude", false, "claude"},
	}
	for _, c := range cases {
		if got := resolveDynamicTool(c.current, c.detected, c.preserveCustom); got != c.want {
			t.Errorf("resolveDynamicTool(%q, %q, %v) = %q, want %q",
				c.current, c.detected, c.preserveCustom, got, c.want)
		}
	}
}

func TestResolveDynamicToolUpgradesShellToOmp(t *testing.T) {
	// A shell session that starts omp inside it should be re-typed as omp
	// (same wrapped-tool upgrade the other builtin CLIs get).
	if got := resolveDynamicTool("shell", "omp", false); got != "omp" {
		t.Errorf("resolveDynamicTool(shell, omp) = %q, want omp", got)
	}
}
