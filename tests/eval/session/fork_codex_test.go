//go:build eval_smoke

package session_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkCodex_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	requireForkTool(t, "codex")
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb)
	repoDir := newForkEvalRepo(t, sb)

	sid := "11111111-2222-3333-4444-555555555555"
	_ = addJSONID(t, sb, "add", "-c", "codex", "-t", "parent", "-g", "evalgrp", "--json", repoDir)
	runBin(t, sb, "session", "set", "parent", "codex-session-id", sid)

	// Seed a rollout so CanForkCodex() passes.
	rollDir := filepath.Join(codexHomeForSandbox(sb), "sessions", "2026", "06", "06")
	mustMkdir(t, rollDir)
	writeFile(t, rollDir, "rollout-20260606T000000-"+sid+".jsonl", "{}\n")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/cx-eval", "-b", "-t", "fork-cx")
	assertForkWorktreeBranch(t, repoDir, "fork/cx-eval", forkOut, forkErr)
}
