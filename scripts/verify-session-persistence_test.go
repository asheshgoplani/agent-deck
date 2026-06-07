package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scriptPath returns the absolute path to the harness under test. Go test runs
// with CWD = package dir (scripts/), so the script is a sibling.
func scriptPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(wd, "verify-session-persistence.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("harness not found at %s: %v", p, err)
	}
	return p
}

// sourceAndRun sources the harness in lib-only mode and runs a bash snippet.
// Returns combined stdout+stderr. `set -e` is active (the harness sets it), so
// snippets must guard non-zero returns with `if`/`||` rather than bare calls.
func sourceAndRun(t *testing.T, env []string, snippet string) (string, error) {
	t.Helper()
	full := "AGENT_DECK_VERIFY_LIB_ONLY=1 source '" + scriptPath(t) + "'\n" + snippet
	cmd := exec.Command("bash", "-c", full)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestIsOwnTmproot_MatchesMktempOutputAnyParent(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/adeck-verify.AbC123", true},                       // Linux mktemp
		{"/var/folders/23/xxxx/T/adeck-verify.AbC123", true},     // macOS $TMPDIR
		{"/private/var/folders/q/y/T/adeck-verify.Z9", true},     // macOS realpath
		{"/tmp", false},                                          // bare tmp — never rm
		{"/home/user/important", false},                          // unrelated dir
		{"", false},                                              // empty
		{"/tmp/other-prefix.123", false},                         // wrong prefix
	}
	for _, c := range cases {
		snippet := `if is_own_tmproot "` + c.path + `"; then echo YES; else echo NO; fi`
		out, err := sourceAndRun(t, nil, snippet)
		if err != nil {
			t.Fatalf("path %q: bash error: %v\n%s", c.path, err, out)
		}
		got := strings.Contains(out, "YES")
		if got != c.want {
			t.Errorf("is_own_tmproot(%q) = %v, want %v (out: %s)", c.path, got, c.want, strings.TrimSpace(out))
		}
	}
}

func TestLibOnly_SourcesWithoutSideEffects(t *testing.T) {
	// Sourcing with LIB_ONLY=1 must NOT run preflight/dispatch: no scenarios,
	// no mktemp side effects, clean exit even with agent-deck absent from PATH.
	out, err := sourceAndRun(t, []string{"PATH=/usr/bin:/bin"},
		`echo "SOURCED_OK"; type -t main >/dev/null && echo "MAIN_DEFINED"`)
	if err != nil {
		t.Fatalf("sourcing failed (expected clean source): %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "SOURCED_OK") {
		t.Fatalf("snippet did not run; got:\n%s", out)
	}
	if !strings.Contains(out, "MAIN_DEFINED") {
		t.Fatalf("main() not defined after source; got:\n%s", out)
	}
	if strings.Contains(out, "persistence harness") || strings.Contains(out, "[PASS]") {
		t.Fatalf("dispatch ran during source (should be gated by LIB_ONLY); got:\n%s", out)
	}
}
