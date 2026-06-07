//go:build eval_smoke

package session_test

import (
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkOpenCode_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	requireForkTool(t, "opencode")
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb)
	repoDir := newForkEvalRepo(t, sb)

	_ = addJSONID(t, sb, "add", "-c", "opencode", "-t", "parent", "-g", "evalgrp", "--json", repoDir)
	runBin(t, sb, "session", "set", "parent", "opencode-session-id", "ses_evalparent")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/oc-eval", "-b", "-t", "fork-oc")
	assertForkWorktreeBranch(t, repoDir, "fork/oc-eval", forkOut, forkErr)
}
