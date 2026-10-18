package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2683 part 3: an ARCHIVED record that still holds a Claude
// conversation id silently won the duplicate-id guard. `add --resume-session`
// then launched a fresh conversation, and `session set ... claude-session-id`
// printed success and reverted. Both must now refuse, naming the archived
// holder and how to free the id. Runs the real binary against a sandboxed HOME.
func TestIssue2683_ArchivedHolderOfClaudeIDIsAnExplicitError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}
	binPath := filepath.Join(t.TempDir(), "agent-deck-2683")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	tmpHome := t.TempDir()
	env := sandboxedCLIEnv(tmpHome)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binPath, args...)
		cmd.Dir = tmpHome
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	const id = "2683aaaa-bbbb-4ccc-8ddd-eeeeffff0000"
	if out, err := run("add", t.TempDir(), "-c", "claude", "-t", "stalled-holder", "--resume-session", id); err != nil {
		t.Fatalf("seed add: %v\n%s", err, out)
	}
	if out, err := run("session", "archive", "stalled-holder"); err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}

	out, err := run("add", t.TempDir(), "-c", "claude", "-t", "fresh-record", "--resume-session", id)
	if err == nil {
		t.Fatalf("add --resume-session accepted an id held by an archived session; output:\n%s", out)
	}
	for _, want := range []string{"archived", "stalled-holder", "unarchive"} {
		if !strings.Contains(out, want) {
			t.Errorf("add refusal does not mention %q; output:\n%s", want, out)
		}
	}

	if out, err := run("add", t.TempDir(), "-c", "claude", "-t", "other-record"); err != nil {
		t.Fatalf("add other: %v\n%s", err, out)
	}
	out, err = run("session", "set", "other-record", "claude-session-id", id)
	if err == nil {
		t.Fatalf("session set accepted an id held by an archived session; output:\n%s", out)
	}
	for _, want := range []string{"archived", "stalled-holder"} {
		if !strings.Contains(out, want) {
			t.Errorf("session set refusal does not mention %q; output:\n%s", want, out)
		}
	}
}
