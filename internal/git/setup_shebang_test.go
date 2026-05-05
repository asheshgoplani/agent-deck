package git

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunWorktreeSetupScript_HonorsShebangWhenExecutable verifies #773:
// when a setup script is marked executable, the kernel's shebang line picks
// the interpreter — not a hard-coded `sh -e`. The script below uses
// `#!/bin/echo` as a sentinel: if the shebang is honored, /bin/echo runs and
// prints the script path on stdout. Under the old `sh -e <path>` behavior,
// the shebang is just a comment and the body would be interpreted as shell.
func TestRunWorktreeSetupScript_HonorsShebangWhenExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang dispatch is POSIX-only")
	}

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "setup-shebang")
	body := "#!/bin/echo SHEBANG_HONORED\nthis line would error under sh -e\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := RunWorktreeSetupScript(scriptPath, 0o755, tmp, tmp, &stdout, &stderr, 5*time.Second)
	if err != nil {
		t.Fatalf("expected success when shebang dispatches /bin/echo: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SHEBANG_HONORED") {
		t.Fatalf("expected /bin/echo via shebang to emit sentinel, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRunWorktreeSetupScript_FallsBackToShellWhenNotExecutable verifies
// backwards compatibility: scripts written before #773 (mode 0644) keep
// running under `sh -e <path>` so existing user setups don't break.
func TestRunWorktreeSetupScript_FallsBackToShellWhenNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "setup-noexec.sh")
	body := "echo legacy-fallback-ran\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := RunWorktreeSetupScript(scriptPath, 0o644, tmp, tmp, &stdout, &stderr, 5*time.Second)
	if err != nil {
		t.Fatalf("expected non-executable script to run via sh fallback: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "legacy-fallback-ran") {
		t.Fatalf("expected fallback to execute body, stdout=%q", stdout.String())
	}
}
