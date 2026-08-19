package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #1946: buildCodexCommand exported CODEX_HOME without shell-quoting it. A
// configured home containing a space was split into an assignment plus a bogus
// command, so Codex never started under the home selected by the resume gate.
func TestIssue1946_CodexHomeWithSpaceSurvivesShell(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CODEX_HOME", "")

	codexHome := filepath.Join(home, "codex config")
	cfg := &UserConfig{Codex: CodexSettings{ConfigDir: codexHome}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nprintf '%s' \"$CODEX_HOME\"\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := &Instance{ID: "i1946", Title: "space-home", Tool: "codex", Command: "codex"}
	command := inst.buildCodexCommand("codex")
	out, err := exec.Command("sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("execute generated Codex command: %v\ncommand: %s\noutput: %s", err, command, out)
	}
	if got := strings.TrimSpace(string(out)); got != codexHome {
		t.Fatalf("CODEX_HOME seen by Codex = %q, want %q\ncommand: %s", got, codexHome, command)
	}
}
