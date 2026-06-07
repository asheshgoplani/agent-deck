package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestOpenCodeForkScriptQuotesWorkDir is the regression guard for the fork-review
// command-safety finding: the generated OpenCode fork bash script must shell-quote
// the working directory (and session id) rather than interpolate them raw, so a
// project path containing shell metacharacters (here a literal double-quote) cannot
// break out of the `cd`. The session id is validated to a safe charset upstream
// (normalizeToolSessionID), so shellescape.Quote leaves it bare — the workDir
// assertion is what proves the quoting is actually applied.
func TestOpenCodeForkScriptQuotesWorkDir(t *testing.T) {
	parent := NewInstanceWithTool("oc", `/tmp/project with "quote"`, "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()

	cmd, err := parent.ForkOpenCodeWithOptions("oc fork", "", nil)
	if err != nil {
		t.Fatalf("ForkOpenCodeWithOptions: %v", err)
	}

	// The command is `bash '<scriptPath>'`.
	scriptPath := strings.TrimPrefix(strings.TrimSuffix(cmd, "'"), "bash '")
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read fork script %q: %v", scriptPath, err)
	}
	t.Cleanup(func() { _ = os.Remove(scriptPath) })
	script := string(body)

	// Unsafe form: raw double-quoting would let the embedded `"` break out of cd.
	if strings.Contains(script, `cd "/tmp/project with "quote""`) {
		t.Fatalf("workDir is embedded unsafely (raw double-quoted):\n%s", script)
	}
	// shellescape.Quote wraps a path containing a double-quote in single quotes.
	if !strings.Contains(script, `cd '/tmp/project with "quote"'`) {
		t.Fatalf("workDir should be shell-quoted via shellescape.Quote:\n%s", script)
	}
	// Validated safe session id flows through Quote unchanged (bare).
	if !strings.Contains(script, `opencode export ses_parent_123`) {
		t.Fatalf("opencode session id should flow through shellescape.Quote in the export command:\n%s", script)
	}
}
