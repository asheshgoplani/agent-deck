package git

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Follow-up to #727: at the git layer, `timeout = 0` now encodes "no
// deadline" via context.Background() — callers that want the legacy 60s
// behaviour must pass a positive duration explicitly (or the session layer
// resolves the default for them). A 2s sleep must complete under a zero
// (unlimited) timeout.
func TestRunWorktreeSetupScript_UnlimitedTimeoutAllowsLongScript(t *testing.T) {
	worktreeDir := t.TempDir()

	script := `#!/bin/sh
sleep 2
echo "done"
`
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	// timeout == 0 → unlimited (no context deadline).
	start := time.Now()
	err := RunWorktreeSetupScript(scriptPath, 0o644, t.TempDir(), worktreeDir, &stdout, &stderr, 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected no error under unlimited timeout, got: %v (stderr: %s)", err, stderr.String())
	}
	// Sanity: the script genuinely ran for ~2s (wasn't short-circuited).
	if elapsed < 1500*time.Millisecond {
		t.Errorf("script finished in %v — expected ~2s real runtime", elapsed)
	}
}
