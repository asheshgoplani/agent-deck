package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T2: RunWorktreeSetupScript honours a caller-supplied timeout (threaded via
// the new timeout parameter, not the historical package-level var). A 1s
// timeout on a `sleep 300` script must fail in well under the legacy 60s
// default.
func TestRunWorktreeSetupScript_HonoursCallerTimeout(t *testing.T) {
	worktreeDir := t.TempDir()

	script := `#!/bin/sh
sleep 300
`
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	start := time.Now()
	err := RunWorktreeSetupScript(scriptPath, t.TempDir(), worktreeDir, &stdout, &stderr, 1*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'timed out' in error, got: %v", err)
	}
	// Generous upper bound — catches a regression where the caller-supplied
	// timeout is ignored and the 60s default is used.
	if elapsed > 30*time.Second {
		t.Errorf("RunWorktreeSetupScript took %v with a 1s timeout; caller timeout is not being threaded", elapsed)
	}
}
