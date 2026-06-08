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

	// Pane resolution yields nothing (unresolved -> empty, exit 0 — NOT an error).
	out, err := sourceAndRun(t,
		[]string{"ARGV_OUT=" + emptyArgv, "PATH=/usr/bin:/bin"},
		`tmux_pane_start_command_for_session() { return 0; }
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

func TestCaptureClaudeArgv_RealResolverErrorPropagates(t *testing.T) {
	// Owner review (line 164): a REAL resolver error (nonzero from
	// tmux_pane_start_command_for_session — e.g. resolve_tmux_session hit a DB
	// error / malformed JSON, exit 1/3) must PROPAGATE out of capture so the
	// scenario can FAIL loudly — not be flattened to empty/exit-0 (which would
	// degrade to [SKIP] on non-stub hosts).
	out, err := sourceAndRun(t, []string{"ARGV_OUT=/dev/null"},
		`tmux_pane_start_command_for_session() { return 7; }
		 rc=0; argv="$(capture_claude_argv s)" || rc=$?
		 echo "rc=${rc} argv=[${argv}]"`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rc=7") {
		t.Fatalf("capture must propagate a real resolver error code; got:\n%s", out)
	}
}

func TestScenario3_RealResolverErrorFailsEvenNonStub(t *testing.T) {
	// Owner review (line 164): a real resolver/interface error during argv
	// capture must FAIL scenario 3 even on a NON-stub host — not degrade to a
	// [SKIP], which would contradict the "non-not-found errors fail loudly"
	// contract. (Unresolved/empty argv still SKIPs on non-stub; see
	// TestArgvUnobservable_FailsInStubModeSkipsOtherwise.)
	out, err := sourceAndRun(t, []string{"S3_TMPROOT=" + t.TempDir()}, `
FAILED=0
USE_STUB=0
SESSION_PREFIX=verify-persist-test
TMPROOT="${S3_TMPROOT}"
ARGV_OUT="${TMPROOT}/argv.log"
agent-deck() { return 0; }
tmux() { return 0; }
sleep() { :; }
capture_claude_argv() { printf 'ERROR: db locked\n' >&2; return 1; }
scenario_3_restart_resume
echo "FAILED=${FAILED}"
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "FAILED=1") {
		t.Fatalf("real resolver error must FAIL scenario 3 even non-stub; got:\n%s", out)
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

func TestCleanup_RemovesOnlyTrackedCreatedSessions(t *testing.T) {
	// Review B1: cleanup must remove ONLY the exact titles this invocation
	// created (tracked in CREATED_SESSIONS) — never a prefix/list-parse match. A
	// bare prefix collides (verify-persist-123 matches a foreign
	// verify-persist-1234-*). cleanup must also never call `agent-deck list`
	// (the unsafe text-parse path is removed entirely).
	record := filepath.Join(t.TempDir(), "calls.log")
	out, err := sourceAndRun(t, []string{"RECORD=" + record}, `
LOGINSIM_SCOPE=adeck-verify-loginsim-test
TMPROOT=""
CREATED_SESSIONS=("verify-persist-123-s1" "verify-persist-123-s5")
agent-deck() {
  printf '%s\n' "$*" >> "${RECORD}.all"
  if [[ "$1" == "session" && "$2" == "stop" ]]; then printf 'stop:%s\n' "$3" >> "${RECORD}"; return 0; fi
  if [[ "$1" == "remove" ]]; then printf 'remove:%s\n' "$2" >> "${RECORD}"; return 0; fi
  return 0
}
systemctl() { return 0; }
cleanup
echo "---REMOVED---"; [[ -f "${RECORD}" ]] && cat "${RECORD}" || true
echo "---ALLCALLS---"; [[ -f "${RECORD}.all" ]] && cat "${RECORD}.all" || true
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	for _, want := range []string{
		"stop:verify-persist-123-s1", "remove:verify-persist-123-s1",
		"stop:verify-persist-123-s5", "remove:verify-persist-123-s5",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup must remove tracked session %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "list") {
		t.Fatalf("cleanup must NOT call `agent-deck list` (unsafe prefix-parse path removed); got:\n%s", out)
	}
}

func TestCleanup_NoOpWhenNothingCreated(t *testing.T) {
	// Review B2: on a failed preflight (e.g. missing jq) the EXIT trap fires
	// having created nothing — CREATED_SESSIONS is empty, so cleanup must remove
	// NO sessions (no false-deletion of pre-existing matching sessions).
	record := filepath.Join(t.TempDir(), "calls.log")
	out, err := sourceAndRun(t, []string{"RECORD=" + record}, `
LOGINSIM_SCOPE=adeck-verify-loginsim-test
TMPROOT=""
CREATED_SESSIONS=()
agent-deck() { printf 'CALLED:%s\n' "$*" >> "${RECORD}"; return 0; }
systemctl() { return 0; }
cleanup
echo "DONE"; [[ -f "${RECORD}" ]] && cat "${RECORD}" || true
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DONE") {
		t.Fatalf("cleanup did not complete; got:\n%s", out)
	}
	if strings.Contains(out, "CALLED:") {
		t.Fatalf("cleanup with empty CREATED_SESSIONS must remove nothing; got:\n%s", out)
	}
}

func TestCreateAndStartSession_TracksCreatedTitle(t *testing.T) {
	// Wiring: a successful `agent-deck add` records the exact title in
	// CREATED_SESSIONS so cleanup can later remove exactly it.
	out, err := sourceAndRun(t, []string{"S_TMPROOT=" + t.TempDir()}, `
CREATED_SESSIONS=()
TMPROOT="${S_TMPROOT}"
agent-deck() { return 0; }
create_and_start_session "verify-persist-999-s1" claude
printf 'TRACKED=[%s]\n' "${CREATED_SESSIONS[*]:-}"
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "TRACKED=[verify-persist-999-s1]") {
		t.Fatalf("create_and_start_session must track the created title; got:\n%s", out)
	}
}

func TestResolveTmuxSession_RealErrorSurfaces(t *testing.T) {
	// Review ALSO: `session show` exits 2 for genuine not-found (swallow) but
	// exit 1 for a real error (DB/load/permission). A real error must SURFACE
	// (nonzero + message), not silently degrade to empty -> false-green [SKIP].
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; resolver depends on jq")
	}
	dir := t.TempDir()
	fake := `#!/usr/bin/env bash
if [[ "$1" == "session" && "$2" == "show" ]]; then
  echo "load error: database is locked" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := sourceAndRun(t,
		[]string{"PATH=" + dir + ":" + os.Getenv("PATH")},
		`if msg="$(resolve_tmux_session foo 2>&1)"; then
		   echo "UNEXPECTED_OK=[$msg]"
		 else
		   echo "STATUS=$? MSG=[$msg]"
		 fi`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if strings.Contains(out, "UNEXPECTED_OK") {
		t.Fatalf("a real session-show error was swallowed as success (false-green):\n%s", out)
	}
	if !strings.Contains(out, "STATUS=1") || !strings.Contains(strings.ToLower(out), "failed") {
		t.Fatalf("real session-show error must surface as nonzero + message; got:\n%s", out)
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

func TestResolveTmuxSession_MalformedJSONSurfacesAsError(t *testing.T) {
	// Repo-owner review P2 (PR #1309): when `agent-deck session show --json`
	// SUCCEEDS but returns malformed JSON, that is a real broken-interface
	// breakage. The resolver must SURFACE it (nonzero status + explicit error)
	// rather than swallow it into an empty name that degrades to a false-green
	// [SKIP]. Only the EXPECTED agent-deck not-found nonzero is swallowed
	// (covered by TestResolveTmuxSession_NonzeroAgentDeckDoesNotAbortUnderSetE).
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; resolver depends on jq")
	}
	dir := t.TempDir()
	fake := `#!/usr/bin/env bash
if [[ "$1" == "session" && "$2" == "show" ]]; then
  printf '{ this is not valid json\n'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := sourceAndRun(t,
		[]string{"PATH=" + dir + ":" + os.Getenv("PATH")},
		`if msg="$(resolve_tmux_session foo 2>&1)"; then
		   echo "UNEXPECTED_OK=[$msg]"
		 else
		   echo "STATUS=$? MSG=[$msg]"
		 fi`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if strings.Contains(out, "UNEXPECTED_OK") {
		t.Fatalf("malformed JSON was silently swallowed as success (false-green):\n%s", out)
	}
	if !strings.Contains(out, "STATUS=3") || !strings.Contains(strings.ToLower(out), "malformed json") {
		t.Fatalf("malformed JSON must surface as an explicit nonzero error; got:\n%s", out)
	}
}

func TestArgvUnobservable_FailsInStubModeSkipsOtherwise(t *testing.T) {
	// Repo-owner review P1 (PR #1309): an unobservable (empty) claude argv is
	// expected degradation on non-stub hosts (macOS/no-systemd) -> [SKIP], but
	// in stub mode (AGENT_DECK_VERIFY_USE_STUB=1 / USE_STUB=1) the stub MUST
	// have recorded args, so empty means the gate could not verify -> [FAIL].
	// A [SKIP] there would be a false-green on the mandatory gate.
	stub, err := sourceAndRun(t, nil,
		`FAILED=0; USE_STUB=1; argv_unobservable 3 "resume shape"; echo "FAILED=${FAILED}"`)
	if err != nil {
		t.Fatalf("bash error (stub): %v\n%s", err, stub)
	}
	if !strings.Contains(stub, "[FAIL]") || !strings.Contains(stub, "FAILED=1") {
		t.Fatalf("stub mode + empty argv must FAIL (false-green guard); got:\n%s", stub)
	}

	nonstub, err := sourceAndRun(t, nil,
		`FAILED=0; USE_STUB=0; argv_unobservable 3 "resume shape"; echo "FAILED=${FAILED}"`)
	if err != nil {
		t.Fatalf("bash error (non-stub): %v\n%s", err, nonstub)
	}
	if !strings.Contains(nonstub, "[SKIP]") || !strings.Contains(nonstub, "FAILED=0") {
		t.Fatalf("non-stub + empty argv must SKIP (expected degradation); got:\n%s", nonstub)
	}
}

func TestScenario3_StubModeUnobservableArgvFailsViaDispatch(t *testing.T) {
	// P1 wiring: proves argv_unobservable actually fires inside the real
	// scenario_3 dispatch path (the helper-only test above doesn't). In stub
	// mode with an unobservable (empty) argv — all agent-deck calls succeed,
	// only the capture comes back empty — scenario 3 must FAIL, not SKIP.
	out, err := sourceAndRun(t, []string{"S3_TMPROOT=" + t.TempDir()}, `
FAILED=0
USE_STUB=1
SESSION_PREFIX=verify-persist-test
TMPROOT="${S3_TMPROOT}"
ARGV_OUT="${TMPROOT}/argv.log"
agent-deck() { return 0; }
tmux() { return 0; }
sleep() { :; }
capture_claude_argv() { :; }   # empty argv -> classify=skip -> argv_unobservable
scenario_3_restart_resume
echo "FAILED=${FAILED}"
`)
	if err != nil {
		t.Fatalf("bash error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "FAILED=1") {
		t.Fatalf("stub-mode unobservable argv must FAIL via scenario_3 dispatch; got:\n%s", out)
	}
}
