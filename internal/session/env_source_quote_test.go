package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSourceCmdInBash runs a buildSourceCmd result (plus an optional tail) with
// bash in dir, with HOME pointed at dir so nothing touches the real home.
func runSourceCmdInBash(t *testing.T, dir, cmd string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	c := exec.Command(bash, "-c", cmd)
	c.Dir = dir
	c.Env = []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH")}
	out, err := c.CombinedOutput()
	return string(out), err
}

// TestBuildSourceCmd_PathIsNotShellEvaluated: an env_file path is data, not
// shell. Command substitution in the path must not run.
func TestBuildSourceCmd_PathIsNotShellEvaluated(t *testing.T) {
	for _, ignoreMissing := range []bool{true, false} {
		for _, form := range []string{"$(touch %s)", "`touch %s`"} {
			dir := t.TempDir()
			pwned := filepath.Join(dir, "PWNED")
			payload := strings.Replace(form, "%s", pwned, 1)
			path := filepath.Join(dir, "x"+payload+".env")
			cmd := buildSourceCmd(path, ignoreMissing)
			_, _ = runSourceCmdInBash(t, dir, cmd)
			if _, err := os.Stat(pwned); err == nil {
				t.Errorf("ignoreMissing=%v payload %q: path was shell-evaluated (PWNED created)\ncmd: %s", ignoreMissing, payload, cmd)
			}
		}
	}
}

// TestBuildSourceCmd_SourcesPathWithSpacesAndQuotes: a real env file whose
// path contains spaces and both quote kinds is still sourced.
func TestBuildSourceCmd_SourcesPathWithSpacesAndQuotes(t *testing.T) {
	for _, ignoreMissing := range []bool{true, false} {
		dir := t.TempDir()
		sub := filepath.Join(dir, `my "env" dir's`)
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(sub, "a b.env")
		if err := os.WriteFile(path, []byte("export LOC5_SENTINEL=sourced\n"), 0o600); err != nil {
			t.Fatalf("write env file: %v", err)
		}
		cmd := buildSourceCmd(path, ignoreMissing) + ` && printf '%s' "$LOC5_SENTINEL"`
		out, err := runSourceCmdInBash(t, dir, cmd)
		if err != nil || strings.TrimSpace(out) != "sourced" {
			t.Errorf("ignoreMissing=%v: env file not sourced (err=%v, out=%q)\ncmd: %s", ignoreMissing, err, out, cmd)
		}
	}
}
