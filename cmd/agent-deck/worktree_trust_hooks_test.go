package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// newTrustHooksRepo makes a git repo with a setup hook and an isolated trust
// store, returning the repo path.
func newTrustHooksRepo(t *testing.T, hookBody string) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	dir := filepath.Join(repo, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worktree-setup.sh"), []byte(hookBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func setupTrustStatus(t *testing.T, repo string) git.ScriptTrustStatus {
	t.Helper()
	id, err := git.InspectWorktreeScript(repo, "setup")
	if err != nil || id == nil {
		t.Fatalf("inspect: id=%v err=%v", id, err)
	}
	status, err := git.WorktreeScriptTrustStatus(id)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestWorktreeTrustHooks_Yes_ShowsAndTrusts(t *testing.T) {
	repo := newTrustHooksRepo(t, "#!/bin/sh\necho installing-deps\n")
	var out, errOut bytes.Buffer
	code := runWorktreeTrustHooks([]string{repo, "--hook", "setup", "--yes"}, strings.NewReader(""), &out, &errOut, false)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	for _, want := range []string{"echo installing-deps", "sha256:", "sh -e", "Trusted the setup hook"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if got := setupTrustStatus(t, repo); got != git.ScriptTrusted {
		t.Errorf("status = %v, want trusted", got)
	}

	// Second run reports it is already trusted.
	out.Reset()
	if code := runWorktreeTrustHooks([]string{repo, "--yes"}, strings.NewReader(""), &out, &errOut, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Already trusted") {
		t.Errorf("expected Already trusted, got:\n%s", out.String())
	}
}

func TestWorktreeTrustHooks_NoTTYNoYes_Refuses(t *testing.T) {
	repo := newTrustHooksRepo(t, "#!/bin/sh\necho hi\n")
	var out, errOut bytes.Buffer
	code := runWorktreeTrustHooks([]string{repo}, strings.NewReader("y\n"), &out, &errOut, false)
	if code == 0 || !strings.Contains(errOut.String(), "--yes") {
		t.Fatalf("expected refusal naming --yes, exit %d stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "echo hi") {
		t.Errorf("the hook should still be shown, got:\n%s", out.String())
	}
	if got := setupTrustStatus(t, repo); got != git.ScriptUntrusted {
		t.Errorf("status = %v, want untrusted", got)
	}
}

func TestWorktreeTrustHooks_TTY_AsksYesNo(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   git.ScriptTrustStatus
	}{
		{"y\n", git.ScriptTrusted},
		{"n\n", git.ScriptUntrusted},
		{"\n", git.ScriptUntrusted},
	} {
		t.Run(strings.TrimSpace(tc.answer), func(t *testing.T) {
			repo := newTrustHooksRepo(t, "#!/bin/sh\necho hi\n")
			var out, errOut bytes.Buffer
			if code := runWorktreeTrustHooks([]string{repo}, strings.NewReader(tc.answer), &out, &errOut, true); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("expected a y/N question, got:\n%s", out.String())
			}
			if got := setupTrustStatus(t, repo); got != tc.want {
				t.Errorf("status = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorktreeTrustHooks_HookFilterAndRevoke(t *testing.T) {
	repo := newTrustHooksRepo(t, "#!/bin/sh\necho hi\n")
	var out, errOut bytes.Buffer
	if code := runWorktreeTrustHooks([]string{repo, "--hook", "destruction", "--yes"}, strings.NewReader(""), &out, &errOut, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := setupTrustStatus(t, repo); got != git.ScriptUntrusted {
		t.Errorf("--hook destruction must not trust the setup hook, status = %v", got)
	}
	if code := runWorktreeTrustHooks([]string{repo, "--hook", "bogus"}, strings.NewReader(""), &out, &errOut, false); code == 0 {
		t.Error("expected an invalid --hook to fail")
	}

	if code := runWorktreeTrustHooks([]string{repo, "--yes"}, strings.NewReader(""), &out, &errOut, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out.Reset()
	if code := runWorktreeTrustHooks([]string{repo, "--revoke"}, strings.NewReader(""), &out, &errOut, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "Revoked trust for the setup hook") {
		t.Errorf("revoke output:\n%s", out.String())
	}
	if got := setupTrustStatus(t, repo); got != git.ScriptUntrusted {
		t.Errorf("status after revoke = %v", got)
	}
}

func TestPartitionWorktreeTrustScriptsArgs_HookValue(t *testing.T) {
	flags, pos := partitionWorktreeTrustScriptsArgs([]string{".", "--hook", "setup", "--yes"})
	if strings.Join(flags, " ") != "--hook setup --yes" || strings.Join(pos, " ") != "." {
		t.Errorf("flags=%v positional=%v", flags, pos)
	}
}

func TestExtractAllowRepoScriptsFlag(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		allow, trust bool
		rest         string
	}{
		{[]string{"launch", ".", "--run-hooks"}, true, false, "launch ."},
		{[]string{"launch", ".", "--allow-repo-scripts"}, true, false, "launch ."},
		{[]string{"launch", "--run-hooks", "--trust", "."}, true, true, "launch ."},
		{[]string{"launch", "--run-hooks=false", "."}, false, false, "launch ."},
		// --trust is left alone without a run-hooks flag.
		{[]string{"launch", "--trust", "."}, false, false, "launch --trust ."},
	} {
		allow, trust, rest := extractAllowRepoScriptsFlag(tc.args)
		if allow != tc.allow || trust != tc.trust || strings.Join(rest, " ") != tc.rest {
			t.Errorf("extract(%v) = (%v, %v, %v), want (%v, %v, %s)", tc.args, allow, trust, rest, tc.allow, tc.trust, tc.rest)
		}
	}
}
