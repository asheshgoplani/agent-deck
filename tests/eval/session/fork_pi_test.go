//go:build eval_smoke

package session_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkPi_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	requireForkTool(t, "pi")
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb) // [worktree] branch_prefix="" + sibling location

	repoDir := newForkEvalRepo(t, sb)

	// Register a Pi parent and capture its instance ID from --json.
	id := addJSONID(t, sb, "add", "-c", "pi", "-t", "parent", "-g", "evalgrp", "--json", repoDir)

	// Satisfy CanForkPi(): seed a session JSONL under ~/.pi/agent-deck/<id>/.
	piDir := filepath.Join(sb.Home, ".pi", "agent-deck", id)
	mustMkdir(t, piDir)
	writeFile(t, piDir, "session.jsonl", "{}\n")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/pi-eval", "-b", "-t", "fork-pi")

	assertForkWorktreeBranch(t, repoDir, "fork/pi-eval", forkOut, forkErr)
}

func newForkEvalRepo(t *testing.T, sb *harness.Sandbox) string {
	t.Helper()
	repoDir := filepath.Join(sb.Home, "proj")
	mustMkdir(t, repoDir)
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")
	return repoDir
}

func writeForkConfig(t *testing.T, sb *harness.Sandbox) {
	t.Helper()
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	mustMkdir(t, cfgDir)
	socketName := fmt.Sprintf("ad-fork-%x", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socketName, "kill-server").Run()
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`[tmux]
socket_name = "`+socketName+`"

[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write fork eval config: %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func addJSONID(t *testing.T, sb *harness.Sandbox, args ...string) string {
	t.Helper()
	out, err := runBinTry(sb, args...)
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, out)
	}
	// `add --json` emits pretty-printed (json.MarshalIndent) MULTI-LINE JSON, so a
	// per-line "{...}" scan never matches. Slice the whole output from the first
	// '{' to the last '}' and unmarshal that. (Empirically verified: the older
	// line-by-line scanner t.Fatalf'd on every invocation.)
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		t.Fatalf("agent-deck %v emitted no JSON object; output:\n%s", args, out)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &payload); err != nil {
		t.Fatalf("agent-deck %v: parse JSON %q: %v", args, out[start:end+1], err)
	}
	if payload.ID == "" {
		t.Fatalf("agent-deck %v JSON has empty id; output:\n%s", args, out)
	}
	return payload.ID
}

func assertForkWorktreeBranch(t *testing.T, repoDir, branch, forkOut string, forkErr error) {
	t.Helper()
	if forkErr != nil {
		t.Fatalf("session fork failed before tool fork completed.\nerr: %v\noutput:\n%s", forkErr, forkOut)
	}
	forkPath := worktreePathForBranch(t, repoDir, branch)
	if forkPath == "" {
		t.Fatalf("destination worktree for %s not found.\nerr: %v\noutput:\n%s", branch, forkErr, forkOut)
	}
	gotBranch := strings.TrimSpace(gitOut(t, forkPath, "rev-parse", "--abbrev-ref", "HEAD"))
	if gotBranch != branch {
		t.Errorf("destination branch = %q, want %s", gotBranch, branch)
	}
}

func requireForkTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

func codexHomeForSandbox(sb *harness.Sandbox) string {
	for _, kv := range sb.Env() {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			if v := strings.TrimSpace(strings.TrimPrefix(kv, "CODEX_HOME=")); v != "" {
				return v
			}
		}
	}
	return filepath.Join(sb.Home, ".codex")
}
