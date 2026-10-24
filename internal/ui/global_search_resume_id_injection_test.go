package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The global-search hit's SessionID is the "sessionId" field of an indexed
// Claude transcript (recall NativeID), i.e. file data that anyone able to
// write a .jsonl under an indexed config root controls. Opening the hit bakes
// it into inst.Command, which runs as a `bash -c` payload, so it must be
// quoted like every other claude resume id.
func globalSearchCommandFor(t *testing.T, sid string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	// Mirrors createSessionFromGlobalSearch.
	inst := session.NewInstanceWithGroupAndTool("project", project, "", "claude")
	inst.ClaudeSessionID = sid
	session.MarkClaudeSessionIDVerified(inst)
	return globalSearchClaudeCommand(inst, sid, &session.ClaudeOptions{})
}

func TestGlobalSearchResumeIDQuoting_Injection(t *testing.T) {
	// The $(...) sits inside the `exec claude` word itself, so it runs even
	// though exec replaces the shell before the `; touch` part is reached.
	const evil = "abc$(touch PWNED); touch PWNED"
	cmdStr := globalSearchCommandFor(t, evil)

	runDir, binDir := t.TempDir(), t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shellescape.Quote(record) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(runDir, "PWNED")); err == nil {
		t.Errorf("injected command executed: PWNED created\ncmd: %s", cmdStr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub claude never ran: %v (run err %v, output %s)\ncmd: %s", err, runErr, out, cmdStr)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(args) < 2 || args[0] != "--resume" || args[1] != evil {
		t.Errorf("argv = %q, want --resume %q as one argument\ncmd: %s", args, evil, cmdStr)
	}
}

func TestGlobalSearchResumeIDQuoting_OrdinaryUUIDUnchanged(t *testing.T) {
	const sid = "3f2b8c1e-9a4d-4e7f-b6c5-0d1e2f3a4b5c"
	if got, want := globalSearchCommandFor(t, sid), " exec claude --resume "+sid; !strings.HasSuffix(got, want) {
		t.Errorf("command = %q, want it to end with %q", got, want)
	}
}
