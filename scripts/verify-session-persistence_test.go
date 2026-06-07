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

func fakeClaudePath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(wd, "verify-session-persistence.d", "fake-claude.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fake claude not found at %s: %v", p, err)
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
		{"/tmp/adeck-verify.AbC123", true},                   // Linux mktemp
		{"/var/folders/23/xxxx/T/adeck-verify.AbC123", true}, // macOS $TMPDIR
		{"/private/var/folders/q/y/T/adeck-verify.Z9", true}, // macOS realpath
		{"/tmp", false},                  // bare tmp — never rm
		{"/home/user/important", false},  // unrelated dir
		{"", false},                      // empty
		{"/tmp/other-prefix.123", false}, // wrong prefix
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
		{"unknown", "claude --session-id abc", "fail"}, // no silent empty verdict
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

func TestResolveTmuxSession_MissingJqIsExplicitError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/bin/cat", filepath.Join(dir, "cat")); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/bash
if [[ "$1" == "session" && "$2" == "show" ]]; then
  printf '{ "tmux_session": "agentdeck_missing_jq" }\n'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := sourceAndRun(t,
		[]string{"PATH=" + dir},
		`if msg="$(resolve_tmux_session foo 2>&1)"; then
		   echo "UNEXPECTED_OK=[$msg]"
		 else
		   echo "STATUS=$?"
		   echo "MSG=[$msg]"
		 fi`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if strings.Contains(out, "UNEXPECTED_OK") {
		t.Fatalf("missing jq was silently treated as success:\n%s", out)
	}
	if !strings.Contains(out, "STATUS=2") || !strings.Contains(out, "jq binary not on PATH") {
		t.Fatalf("missing jq must be explicit status 2 error; got:\n%s", out)
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

func TestScenario3_RestartFailureMarksFailure(t *testing.T) {
	out, err := sourceAndRun(t, nil, `
FAILED=0
SESSION_PREFIX=verify-persist-test
TMPROOT="${TMPDIR:-/tmp}/adeck-verify-test-s3"
mkdir -p "${TMPROOT}"
ARGV_OUT="${TMPROOT}/argv.log"
start_count=0
agent-deck() {
  if [[ "$1" == "session" && "$2" == "start" ]]; then
    start_count=$((start_count + 1))
    if [[ "${start_count}" -eq 2 ]]; then
      return 42
    fi
  fi
  return 0
}
sleep() { :; }
capture_claude_argv() { :; }
scenario_3_restart_resume
echo "FAILED=${FAILED}"
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "FAILED=1") {
		t.Fatalf("restart command failure must mark scenario failed; got:\n%s", out)
	}
}

func TestScenario5_ReviveFailureMarksFailure(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "pgrep-count")
	if err := os.WriteFile(counter, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := sourceAndRun(t, []string{"COUNT_FILE=" + counter}, `
FAILED=0
SESSION_PREFIX=verify-persist-test
TMPROOT="${TMPDIR:-/tmp}/adeck-verify-test-s5"
ARGV_OUT="${TMPROOT}/argv.log"
agent-deck() {
  if [[ "$1" == "session" && "$2" == "revive" ]]; then
    return 42
  fi
  return 0
}
resolve_tmux_session() { printf 'agentdeck_fake_session\n'; }
pgrep() {
  c="$(cat "${COUNT_FILE}")"
  c=$((c + 1))
  printf '%s' "${c}" > "${COUNT_FILE}"
  if [[ "${c}" -eq 1 ]]; then
    printf '12345\n'
    return 0
  fi
  return 1
}
kill() { return 0; }
sleep() { :; }
scenario_5_reviver_respawns_killed_pipe
echo "FAILED=${FAILED}"
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "FAILED=1") {
		t.Fatalf("revive command failure must mark scenario failed; got:\n%s", out)
	}
}

func TestCleanup_RemovesFullTitlesFromJSONList(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; cleanup JSON path depends on jq")
	}
	record := filepath.Join(t.TempDir(), "calls.log")
	out, err := sourceAndRun(t, []string{"RECORD=" + record}, `
SESSION_PREFIX=verify-persist-123456
LOGINSIM_SCOPE=adeck-verify-loginsim-test
TMPROOT=""
agent-deck() {
  if [[ "$1" == "list" && "${2:-}" == "--json" ]]; then
    cat <<'JSON'
[
  {"title":"verify-persist-123456-s1","id":"id-1"},
  {"title":"unrelated","id":"id-2"}
]
JSON
    return 0
  fi
  if [[ "$1" == "list" ]]; then
    printf 'TITLE                GROUP           PATH                                     ID\n'
    printf 'verify-persist-12... T               /tmp/path                                id-1\n'
    return 0
  fi
  if [[ "$1" == "session" && "$2" == "stop" ]]; then
    printf 'stop:%s\n' "$3" >> "${RECORD}"
    return 0
  fi
  if [[ "$1" == "remove" ]]; then
    printf 'remove:%s\n' "$2" >> "${RECORD}"
    return 0
  fi
  return 0
}
systemctl() { return 0; }
cleanup
if [[ -f "${RECORD}" ]]; then cat "${RECORD}"; fi
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stop:verify-persist-123456-s1") ||
		!strings.Contains(out, "remove:verify-persist-123456-s1") {
		t.Fatalf("cleanup must use full JSON titles instead of truncated table output; got:\n%s", out)
	}
}

func TestFakeClaudeStub_UsesPortableSleepDuration(t *testing.T) {
	dir := t.TempDir()
	sleepArg := filepath.Join(dir, "sleep-arg.log")
	fakeSleep := `#!/usr/bin/env bash
printf '%s\n' "$*" > "${SLEEP_ARG_OUT}"
exit 33
`
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(fakeSleep), 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(dir, "argv.log")
	cmd := exec.Command(fakeClaudePath(t), "--session-id", "abc")
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"SLEEP_ARG_OUT="+sleepArg,
		"AGENT_DECK_VERIFY_ARGV_OUT="+argvFile,
	)
	err := cmd.Run()
	if err == nil {
		t.Fatal("fake sleep should stop the stub after the first sleep call")
	}
	data, readErr := os.ReadFile(sleepArg)
	if readErr != nil {
		t.Fatalf("fake sleep was not invoked: %v", readErr)
	}
	got := strings.TrimSpace(string(data))
	if got == "" || strings.Contains(got, "infinity") {
		t.Fatalf("stub must not rely on non-portable 'sleep infinity'; sleep arg = %q", got)
	}
	argv, readErr := os.ReadFile(argvFile)
	if readErr != nil {
		t.Fatalf("argv log was not written: %v", readErr)
	}
	if strings.TrimSpace(string(argv)) != "--session-id abc" {
		t.Fatalf("argv log = %q, want --session-id abc", strings.TrimSpace(string(argv)))
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

func TestScenario_InitialStartFailureMarksFailureNotAbort(t *testing.T) {
	// F2 regression: when a scenario's INITIAL `agent-deck session start` fails,
	// the scenario must banner_fail and return — NOT abort the harness under
	// `set -e` with no diagnostic banner. Exercised via scenario 4 (simplest).
	out, err := sourceAndRun(t, []string{"S4_TMPROOT=" + t.TempDir()}, `
FAILED=0
SESSION_PREFIX=verify-persist-test
TMPROOT="${S4_TMPROOT}"
ARGV_OUT="${TMPROOT}/argv.log"
agent-deck() {
  if [[ "$1" == "session" && "$2" == "start" ]]; then
    return 9
  fi
  return 0
}
tmux() { return 0; }
sleep() { :; }
scenario_4_fresh_session_shape
echo "REACHED_AFTER FAILED=${FAILED}"
`)
	if err != nil {
		t.Fatalf("harness aborted under set -e instead of bannering (F2): %v\n%s", err, out)
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "REACHED_AFTER FAILED=1") {
		t.Fatalf("initial start failure must mark [FAIL] and not abort; got:\n%s", out)
	}
}

func TestCleanup_HandlesNullTitleInJSONList(t *testing.T) {
	// F4 regression: a session entry with a null `.title` must not error the jq
	// filter (startswith on null) and abort the cleanup pass — matching sessions
	// must still be removed. Null entry FIRST so the unguarded filter errors
	// before reaching the matching one.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; cleanup JSON path depends on jq")
	}
	record := filepath.Join(t.TempDir(), "calls.log")
	out, err := sourceAndRun(t, []string{"RECORD=" + record}, `
SESSION_PREFIX=verify-persist-123456
LOGINSIM_SCOPE=adeck-verify-loginsim-test
TMPROOT=""
agent-deck() {
  if [[ "$1" == "list" && "${2:-}" == "--json" ]]; then
    cat <<'JSON'
[
  {"title":null,"id":"id-null"},
  {"title":"verify-persist-123456-s1","id":"id-1"}
]
JSON
    return 0
  fi
  if [[ "$1" == "session" && "$2" == "stop" ]]; then printf 'stop:%s\n' "$3" >> "${RECORD}"; return 0; fi
  if [[ "$1" == "remove" ]]; then printf 'remove:%s\n' "$2" >> "${RECORD}"; return 0; fi
  return 0
}
systemctl() { return 0; }
cleanup
if [[ -f "${RECORD}" ]]; then cat "${RECORD}"; fi
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stop:verify-persist-123456-s1") ||
		!strings.Contains(out, "remove:verify-persist-123456-s1") {
		t.Fatalf("cleanup must skip null titles and still remove matching session; got:\n%s", out)
	}
}
