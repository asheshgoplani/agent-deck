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

// writeFakeAgentDeck installs a stub `agent-deck` on PATH that answers
// `session show --json <name>` with a fixed payload. Returns the dir to prepend.
func writeFakeAgentDeck(t *testing.T, tmuxSession string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
if [[ "$1" == "session" && "$2" == "show" ]]; then
  cat <<'JSON'
{ "title": "foo", "tmux_session": "` + tmuxSession + `" }
JSON
  exit 0
fi
exit 0
`
	p := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent-deck: %v", err)
	}
	return dir
}

func TestResolveTmuxSession_UsesShowJson(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; resolver depends on jq")
	}
	const want = "agentdeck_foo_410a3758" // real prefix is agentdeck_, NOT adeck_
	bin := writeFakeAgentDeck(t, want)
	out, err := sourceAndRun(t,
		[]string{"PATH=" + bin + ":" + os.Getenv("PATH")},
		`resolve_tmux_session foo`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != want {
		t.Fatalf("resolve_tmux_session = %q, want %q", strings.TrimSpace(out), want)
	}
}

func TestClassifyArgv_VerdictsIncludingEmptyIsSkip(t *testing.T) {
	// Regression for the reported bug: empty argv (claude unobservable for this
	// session) MUST classify as "skip", not "fail".
	cases := []struct{ mode, argv, want string }{
		{"resume", "", "skip"},
		{"fresh", "", "skip"},
		{"resume", "claude --resume abc", "pass"},
		{"resume", "claude --session-id abc", "pass"},
		{"resume", "claude --foo", "fail"},
		{"fresh", "claude --session-id abc", "pass"},
		{"fresh", "claude --session-id abc --resume x", "fail"}, // both -> wrong shape
		{"fresh", "claude --resume x", "fail"},
	}
	for _, c := range cases {
		snippet := `classify_argv ` + c.mode + ` "` + c.argv + `"`
		out, err := sourceAndRun(t, nil, snippet)
		if err != nil {
			t.Fatalf("mode=%s argv=%q: bash error: %v\n%s", c.mode, c.argv, err, out)
		}
		if strings.TrimSpace(out) != c.want {
			t.Errorf("classify_argv(%s, %q) = %q, want %q", c.mode, c.argv, strings.TrimSpace(out), c.want)
		}
	}
}

func TestCaptureClaudeArgv_PrefersStubFile(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.log")
	if err := os.WriteFile(argvFile, []byte("claude --session-id ABC123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := sourceAndRun(t, []string{"ARGV_OUT=" + argvFile},
		`capture_claude_argv ignored-name`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--session-id ABC123") {
		t.Fatalf("capture did not return stub argv; got: %q", strings.TrimSpace(out))
	}
}

func TestCaptureClaudeArgv_NeverScansHostWideProcesses(t *testing.T) {
	// Regression for the false-FAIL bug: with the stub file empty AND no pane
	// resolution, capture MUST return empty — never a host-wide `ps|grep claude`
	// match. We plant a live foreign process whose argv contains "claude".
	emptyArgv := filepath.Join(t.TempDir(), "argv.log") // exists, zero bytes
	if err := os.WriteFile(emptyArgv, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Foreign process: argv0 = "claude-foreign-decoy", lives for the test.
	foreign := exec.Command("bash", "-c", `exec -a claude-foreign-decoy sleep 30`)
	if err := foreign.Start(); err != nil {
		t.Fatalf("start foreign decoy: %v", err)
	}
	defer func() { _ = foreign.Process.Kill() }()

	// Force pane resolution to yield nothing (no real tmux session named this).
	out, err := sourceAndRun(t,
		[]string{"ARGV_OUT=" + emptyArgv, "PATH=/usr/bin:/bin"},
		`tmux_pane_start_command_for_session() { return 1; }
		 r="$(capture_claude_argv nonexistent-session)"
		 echo "CAPTURED=[$r]"`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CAPTURED=[]") {
		t.Fatalf("capture returned non-empty (host-wide scan leaked?): %s", strings.TrimSpace(out))
	}
	if strings.Contains(out, "claude-foreign-decoy") {
		t.Fatalf("capture matched a FOREIGN process — false-FAIL bug present: %s", strings.TrimSpace(out))
	}
}

func TestResolveTmuxSession_NonzeroAgentDeckDoesNotAbortUnderSetE(t *testing.T) {
	// Regression: agent-deck `session show` exits 2 on not-found. Under the
	// harness's `set -euo pipefail`, a bare `tsess="$(resolve_tmux_session ...)"`
	// in a caller would abort the function instead of degrading to empty/SKIP.
	// resolve_tmux_session must swallow the nonzero exit and yield empty.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; resolver depends on jq")
	}
	dir := t.TempDir()
	// Fake agent-deck that always exits 2 with no output (simulates not-found).
	fake := "#!/usr/bin/env bash\nexit 2\n"
	if err := os.WriteFile(filepath.Join(dir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mimic a caller's bare assignment under set -e, then a marker line. If
	// resolve_tmux_session propagates the nonzero exit, set -e aborts before
	// the marker and `err` is non-nil.
	out, err := sourceAndRun(t,
		[]string{"PATH=" + dir + ":" + os.Getenv("PATH")},
		`tsess="$(resolve_tmux_session somesession)"; echo "REACHED=[$tsess]"`)
	if err != nil {
		t.Fatalf("set -e aborted before marker (regression present): %v\n%s", err, out)
	}
	if !strings.Contains(out, "REACHED=[]") {
		t.Fatalf("expected empty resolution without abort; got: %s", strings.TrimSpace(out))
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
