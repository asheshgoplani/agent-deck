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
