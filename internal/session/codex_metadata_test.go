package session

import (
	"path/filepath"
	"testing"
)

func TestResolvedCodexHomeLocalAndRemote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))

	local := &Instance{Tool: "codex"}
	if got, want := local.ResolvedCodexHome(), filepath.Join(home, "codex-home"); got != want {
		t.Fatalf("ResolvedCodexHome() = %q, want %q", got, want)
	}
	remote := &Instance{Tool: "codex", SSHHost: "build-host"}
	if got := remote.ResolvedCodexHome(); got != "" {
		t.Fatalf("remote ResolvedCodexHome() = %q, want empty", got)
	}
	other := &Instance{Tool: "gemini"}
	if got := other.ResolvedCodexHome(); got != "" {
		t.Fatalf("non-Codex ResolvedCodexHome() = %q, want empty", got)
	}
}
