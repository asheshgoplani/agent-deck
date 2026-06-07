# Harden verify-session-persistence.sh Cross-Platform Degradation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `scripts/verify-session-persistence.sh` degrade *truthfully* on non-systemd hosts (macOS, BSD, any dev box with a real `claude` running) — emitting `[SKIP]` instead of false `[FAIL]`, resolving its tmux session name via the authoritative path, and cleaning up its own temp dir — without touching the Linux+systemd contract (scenario 2) it exists to gate.

**Architecture:** The harness is a Bash script with zero automated tests. We introduce a one-line `AGENT_DECK_VERIFY_LIB_ONLY` guard so its functions can be *sourced* without running the imperative dispatch, then extract three thin, pure-ish predicates (`is_own_tmproot`, `resolve_tmux_session`, `capture_claude_argv`) that become the TDD seams. A new Go test package (`scripts`, matching the repo convention of `exec.Command("bash", ...)`) drives red→green on each fix and runs identically on macOS and Linux CI. Scenario 2 and the cgroup half of scenario 1 stay Linux-gated and untouched.

**Tech Stack:** Bash 3.2+ (macOS ships 3.2 — no associative arrays, no `${x^^}`), Go `testing` + `os/exec`, `jq`, `tmux`, GitHub Actions.

---

## Why these three fixes (evidence)

Confirmed empirically on a macOS dev host (build `agent-deck` → run harness):

| ID | Symptom | Root cause | Fix |
|----|---------|-----------|-----|
| **A** | Scenarios 3 & 4 emit false `[FAIL]` | Stub bypassed by a pre-existing shared tmux daemon → `ARGV_OUT` empty → tier-2 `pane_start_command` empty → fell to tier-3 `ps -ef \| grep '[c]laude'`, which matched an unrelated real Claude process. | Remove the host-wide `ps` tier; capture from session-scoped sources only; empty argv ⇒ `[SKIP]`, never `[FAIL]`. |
| **B** | Temp dir leaks on macOS | `mktemp -d -t adeck-verify.XXXXXX` resolves under `$TMPDIR` (`/var/folders/...`), but the cleanup guard is `"${TMPROOT}" == /tmp/adeck-verify.*` — never matches → `rm -rf` skipped. | Portable explicit-template `mktemp`; guard on basename, not a hardcoded `/tmp` prefix. |
| **C** | Scenario 5 silently `[SKIP]`s | Resolver greps `agent-deck list` for `/^adeck_/`, but `list` never prints the tmux session name **and** the real prefix is `agentdeck_`. | Resolve via `agent-deck session show --json \| jq -r '.tmux_session'` — the path the other helpers already use and which is verified to work. |

Out of scope (do NOT touch): scenario 2 login-teardown logic, the cgroup branch of scenario 1, `internal/**` product code, the `launch_in_user_scope` defaults.

---

## File Structure

- **Modify** `scripts/verify-session-persistence.sh`
  - Add `is_own_tmproot`, `resolve_tmux_session`, `capture_claude_argv`, `classify_argv` helpers.
  - Wrap all imperative top-level code in `main()`; gate it behind `AGENT_DECK_VERIFY_LIB_ONLY`.
  - Portable `mktemp`; consolidate `tmux_pid_for_session` / `tmux_pane_start_command_for_session` / scenario 5 onto `resolve_tmux_session`; scenario 3/4 onto `capture_claude_argv`.
- **Create** `scripts/verify-session-persistence_test.go` (package `scripts`) — bash-sourcing unit tests for A/B/C plus the source-guard.
- **Modify** `.github/workflows/session-persistence.yml` — add a `unit-xplat` job matrix (`ubuntu-latest`, `macos-latest`) running `go test ./scripts/...`.
- **Modify** `docs/SESSION-PERSISTENCE-SPEC.md`, `CHANGELOG.md` — record the degradation-path contract (skills/docs sync discipline).

**Mandate note (CLAUDE.md):** `scripts/verify-session-persistence.sh` is under the v1.5.2 session-persistence mandate. Every commit here MUST keep `go test -run TestPersistence_ ./internal/session/... -race -count=1` green, and the final verification (Task 6) MUST run `bash scripts/verify-session-persistence.sh` end-to-end on a Linux+systemd host. `git commit --no-verify` is FORBIDDEN on these source commits.

---

## Task 1: Make the harness sourceable (the TDD seam)

**Files:**
- Modify: `scripts/verify-session-persistence.sh`
- Create: `scripts/verify-session-persistence_test.go`

The harness currently runs everything at top level — sourcing it would `mktemp`, install traps, `exit 2` on missing `agent-deck`, and run all scenarios. We wrap the imperative tail in `main()` and call it only when not in lib-only mode, leaving function definitions and `readonly` constants importable.

- [ ] **Step 1: Write the failing test**

Create `scripts/verify-session-persistence_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestLibOnly_SourcesWithoutSideEffects -v`
Expected: FAIL — sourcing runs the full harness (prints the "persistence harness" banner / `[PASS]` lines, or exits non-zero on missing `agent-deck`), so the `dispatch ran during source` or non-zero-exit assertion trips.

- [ ] **Step 3: Wrap the imperative tail in `main()` and gate it**

In `scripts/verify-session-persistence.sh`, move every imperative top-level statement into a new `main()` function. Concretely: the runtime state init (`FAILED=0`, `RUN_ID`, `TMPROOT`, `SESSION_PREFIX`, `LOGINSIM_SCOPE`, `ARGV_OUT`, the `export`), the `trap cleanup ...` line, the preflight `command -v` checks, the stub-install block, the checklist `cat`, and the `want_scenario N && scenario_N` dispatch including the final `FAILED`/exit logic. Assignments inside `main()` are intentionally left WITHOUT `local` so `cleanup`/scenario functions still see them as globals.

Replace the original dispatch tail (currently lines ~347-359) and relocate setup into:

```bash
# ---------- entrypoint ----------
main() {
  FAILED=0
  RUN_ID="$$"
  TMPROOT="$(mktemp -d "${TMPDIR:-/tmp}/adeck-verify.XXXXXX")"
  SESSION_PREFIX="verify-persist-${RUN_ID}"
  LOGINSIM_SCOPE="adeck-verify-loginsim-${RUN_ID}"
  ARGV_OUT="${TMPROOT}/argv.log"
  export AGENT_DECK_VERIFY_ARGV_OUT="${ARGV_OUT}"

  trap cleanup EXIT INT TERM

  # ----- preflight -----
  if ! command -v agent-deck >/dev/null 2>&1; then
    printf "${C_RED}ERROR${C_RESET}: agent-deck binary not on PATH.\n" >&2
    exit 2
  fi
  if ! command -v tmux >/dev/null 2>&1; then
    printf "${C_RED}ERROR${C_RESET}: tmux binary not on PATH.\n" >&2
    exit 2
  fi

  # ----- claude stub decision (unchanged logic) -----
  USE_STUB=0
  if [[ "${AGENT_DECK_VERIFY_USE_STUB:-0}" == "1" ]]; then
    USE_STUB=1
  elif ! command -v claude >/dev/null 2>&1; then
    USE_STUB=1
  fi
  if [[ "${USE_STUB}" == "1" ]]; then
    STUB_DIR="$(cd "$(dirname "$0")/verify-session-persistence.d" && pwd)"
    mkdir -p "${TMPROOT}/bin"
    ln -sf "${STUB_DIR}/fake-claude.sh" "${TMPROOT}/bin/claude"
    export PATH="${TMPROOT}/bin:${PATH}"
    log "claude stub: ${STUB_DIR}/fake-claude.sh (argv -> ${ARGV_OUT})"
  else
    log "claude: $(command -v claude) (real)"
  fi

  # ----- checklist -----
  cat <<EOF
==========================================================
verify-session-persistence.sh — v1.5.2 persistence harness
==========================================================
${CHECKLIST}
==========================================================
Each scenario ends with one [PASS], [FAIL], or [SKIP] line.
EOF

  # ----- dispatch -----
  want_scenario 1 && scenario_1_live_session_cgroup
  want_scenario 2 && scenario_2_login_teardown
  want_scenario 3 && scenario_3_restart_resume
  want_scenario 4 && scenario_4_fresh_session_shape
  want_scenario 5 && scenario_5_reviver_respawns_killed_pipe

  if [[ "${FAILED}" -ne 0 ]]; then
    printf "${C_RED}OVERALL: FAIL${C_RESET}\n" >&2
    exit 1
  fi
  printf "${C_GREEN}OVERALL: PASS${C_RESET}\n"
  exit 0
}

# Run only when executed directly. When sourced for unit tests with
# AGENT_DECK_VERIFY_LIB_ONLY=1, expose functions without side effects.
if [[ "${AGENT_DECK_VERIFY_LIB_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
```

Delete the now-relocated original lines: the runtime-state block (orig. 30-36), the `trap` (orig. 58), the preflight (orig. 60-68), the stub block (orig. 70-86), the checklist `cat` (orig. 88-96), and the dispatch/exit tail (orig. 347-359). Leave `set -euo pipefail`, all `readonly` constants, `banner_*`/`log`, `cleanup`, and every `scenario_*`/helper definition at top level.

- [ ] **Step 4: Run test to verify it passes**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestLibOnly_SourcesWithoutSideEffects -v`
Expected: PASS — `SOURCED_OK` + `MAIN_DEFINED` present, no banner/`[PASS]` leakage.

Also confirm the script still executes normally end-to-end is deferred to Task 6; a quick syntax check now:
Run: `bash -n scripts/verify-session-persistence.sh`
Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add scripts/verify-session-persistence.sh scripts/verify-session-persistence_test.go
git commit -m "test(verify-persistence): make harness sourceable via LIB_ONLY guard"
```

---

## Task 2: Fix B — temp-dir cleanup guard (macOS leak)

**Files:**
- Modify: `scripts/verify-session-persistence.sh` (`cleanup`, and the `mktemp` line added in Task 1)
- Modify: `scripts/verify-session-persistence_test.go`

- [ ] **Step 1: Write the failing test**

Append to `scripts/verify-session-persistence_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestIsOwnTmproot -v`
Expected: FAIL — `is_own_tmproot: command not found` (function doesn't exist yet).

- [ ] **Step 3: Add `is_own_tmproot` and rewire `cleanup`**

In `scripts/verify-session-persistence.sh`, add near the other helpers (after `log()`):

```bash
# is_own_tmproot returns 0 iff $1 is a tempdir THIS harness created via mktemp.
# Matches on the leaf name (prefix adeck-verify.), NOT a hardcoded /tmp parent,
# so it works on macOS where mktemp resolves under $TMPDIR (/var/folders/...).
# Uses pure-bash parameter expansion (${p##*/}) — no fork, no BSD-vs-GNU
# `basename --` portability question, works on bash 3.2. Callers add the `-d`
# existence test before rm.
is_own_tmproot() {
  local p="$1"
  [[ -n "$p" && "${p##*/}" == adeck-verify.* ]]
}
```

In `cleanup()`, replace the final guarded `rm` block:

```bash
  # Remove the script's OWN tempdir only (per CLAUDE.md, never rm user state).
  if is_own_tmproot "${TMPROOT:-}" && [[ -d "${TMPROOT}" ]]; then
    rm -rf "${TMPROOT}" 2>/dev/null || true
  fi
```

(The portable `mktemp "${TMPDIR:-/tmp}/adeck-verify.XXXXXX"` was already introduced in Task 1, Step 3.)

- [ ] **Step 4: Run test to verify it passes**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestIsOwnTmproot -v`
Expected: PASS (all 7 cases).

- [ ] **Step 5: Commit**

```bash
git add scripts/verify-session-persistence.sh scripts/verify-session-persistence_test.go
git commit -m "fix(verify-persistence): clean up tempdir on macOS (basename guard, portable mktemp)"
```

---

## Task 3: Fix C — scenario 5 tmux-name resolver

**Files:**
- Modify: `scripts/verify-session-persistence.sh` (`resolve_tmux_session`, `tmux_pid_for_session`, `tmux_pane_start_command_for_session`, `scenario_5_*`)
- Modify: `scripts/verify-session-persistence_test.go`

- [ ] **Step 1: Write the failing test**

Append to `scripts/verify-session-persistence_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestResolveTmuxSession -v`
Expected: FAIL — `resolve_tmux_session: command not found`.

- [ ] **Step 3: Add `resolve_tmux_session`, consolidate callers, fix scenario 5**

In `scripts/verify-session-persistence.sh`, add (after `is_own_tmproot`):

```bash
# resolve_tmux_session prints the tmux session name agent-deck assigned to the
# managed session $1, via the authoritative `session show --json` path. Empty
# output means unresolved. This is the ONE resolver; helpers and scenario 5 all
# use it (the old `agent-deck list | awk /^adeck_/` parse was doubly wrong: list
# never prints the tmux name, and the real prefix is `agentdeck_`).
resolve_tmux_session() {
  agent-deck session show --json "$1" 2>/dev/null | jq -r '.tmux_session // empty' 2>/dev/null
}
```

Rewrite `tmux_pid_for_session` to use it:

```bash
tmux_pid_for_session() {
  local name="$1"
  local tsess
  tsess="$(resolve_tmux_session "${name}")"
  if [[ -z "${tsess}" || "${tsess}" == "null" ]]; then
    pgrep -f "tmux.*${name}" | head -1 || true
    return
  fi
  tmux display-message -t "${tsess}" -p -F '#{pid}' 2>/dev/null || true
}
```

Rewrite `tmux_pane_start_command_for_session` to use it:

```bash
tmux_pane_start_command_for_session() {
  local name="$1"
  local tsess
  tsess="$(resolve_tmux_session "${name}")"
  if [[ -z "${tsess}" || "${tsess}" == "null" ]]; then
    return 1
  fi
  tmux list-panes -t "${tsess}" -F '#{pane_start_command}' 2>/dev/null | head -1 || true
}
```

In `scenario_5_reviver_respawns_killed_pipe`, replace the `agent-deck list | awk` block:

```bash
  # Resolve via the authoritative path (was: list|awk /^adeck_/ — never matched).
  local tmux_name
  tmux_name="$(resolve_tmux_session "${name}")"
  if [[ -z "${tmux_name}" || "${tmux_name}" == "null" ]]; then
    banner_skip "[5] could not resolve tmux session name for ${name} — skipping reviver scenario"
    agent-deck session stop "${name}" >/dev/null 2>&1 || true
    return
  fi
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run TestResolveTmuxSession -v`
Expected: PASS.

Regression-guard the whole file:
Run: `bash -n scripts/verify-session-persistence.sh`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add scripts/verify-session-persistence.sh scripts/verify-session-persistence_test.go
git commit -m "fix(verify-persistence): resolve tmux session via show --json (scenario 5)"
```

---

## Task 4: Fix A — argv capture never false-FAILs

**Files:**
- Modify: `scripts/verify-session-persistence.sh` (`capture_claude_argv`, `scenario_3_*`, `scenario_4_*`)
- Modify: `scripts/verify-session-persistence_test.go`

- [ ] **Step 1: Write the failing tests**

This task has two seams: `capture_claude_argv` (where argv comes from) and
`classify_argv` (the pass/fail/skip verdict). The `classify_argv` test is the
one that maps directly to the user's reported symptom — *scenario 3/4 emitting
`[FAIL]`* — by asserting that an empty (unobservable) argv yields `skip`, never
`fail`.

Append to `scripts/verify-session-persistence_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run 'TestClassifyArgv|TestCaptureClaudeArgv' -v`
Expected: FAIL — `classify_argv: command not found` and `capture_claude_argv: command not found`.

- [ ] **Step 3: Add `capture_claude_argv` + `classify_argv`; rewire scenarios 3 & 4**

In `scripts/verify-session-persistence.sh`, add (after `resolve_tmux_session`):

```bash
# capture_claude_argv prints the claude argv for managed session $1 using ONLY
# session-scoped sources: (1) the stub's ARGV_OUT file when populated, else
# (2) the tmux pane_start_command for this session. It NEVER scans host-wide
# `ps` — that matched unrelated claude processes and produced false FAILs.
# Empty output means "unobservable for THIS session"; callers MUST treat that
# as SKIP, not FAIL.
capture_claude_argv() {
  local name="$1"
  if [[ -s "${ARGV_OUT:-/dev/null}" ]]; then
    cat "${ARGV_OUT}"
    return 0
  fi
  tmux_pane_start_command_for_session "${name}" 2>/dev/null || true
}

# classify_argv echoes the verdict (skip|pass|fail) for a captured argv under a
# given mode. Empty argv (the session's claude was unobservable) is ALWAYS skip
# — never fail; that is the cross-platform-degradation contract. Extracted so
# the verdict is unit-testable in isolation and shared by scenarios 3 and 4.
#   mode=resume: pass iff argv has --resume OR --session-id.
#   mode=fresh : pass iff argv has --session-id AND NOT --resume.
classify_argv() {
  local mode="$1" argv="$2"
  if [[ -z "${argv}" ]]; then echo skip; return; fi
  case "${mode}" in
    resume)
      if echo "${argv}" | grep -qE -- '--resume|--session-id'; then echo pass; else echo fail; fi
      ;;
    fresh)
      if echo "${argv}" | grep -qE -- '--session-id' && ! echo "${argv}" | grep -qE -- '--resume'; then
        echo pass
      else
        echo fail
      fi
      ;;
  esac
}
```

In `scenario_3_restart_resume`, replace the argv-capture block (the `local argv=""` … fallback chain) and its assertion:

```bash
  local argv verdict
  argv="$(capture_claude_argv "${name}")"
  log "captured claude argv: ${argv}"
  verdict="$(classify_argv resume "${argv}")"
  case "${verdict}" in
    skip) banner_skip "[3] argv unobservable for ${name} (stub not exercised / empty pane_start_command — e.g. pre-existing tmux daemon); cannot assert resume shape" ;;
    pass) banner_pass "[3] restart spawned claude with --resume or --session-id" ;;
    fail) banner_fail "[3] restart spawned claude WITHOUT --resume or --session-id: ${argv}" ;;
  esac
```

In `scenario_4_fresh_session_shape`, replace its argv-capture block and assertion:

```bash
  local argv verdict
  argv="$(capture_claude_argv "${name}")"
  log "captured claude argv: ${argv}"
  verdict="$(classify_argv fresh "${argv}")"
  case "${verdict}" in
    skip) banner_skip "[4] argv unobservable for ${name} (stub not exercised / empty pane_start_command); cannot assert fresh-session shape" ;;
    pass) banner_pass "[4] fresh session uses --session-id without --resume" ;;
    fail) banner_fail "[4] fresh session argv shape wrong: ${argv}" ;;
  esac
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -run 'TestClassifyArgv|TestCaptureClaudeArgv' -v`
Expected: PASS — `TestClassifyArgv_VerdictsIncludingEmptyIsSkip` (8 cases, incl. empty→skip), both `TestCaptureClaudeArgv_*` subtests.

Full unit suite + syntax:
Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/ -v && bash -n scripts/verify-session-persistence.sh`
Expected: all PASS, syntax exit 0.

- [ ] **Step 5: Commit**

```bash
git add scripts/verify-session-persistence.sh scripts/verify-session-persistence_test.go
git commit -m "fix(verify-persistence): SKIP not FAIL when claude argv is unobservable"
```

---

## Task 5: Cross-platform CI + docs/spec sync

**Files:**
- Modify: `.github/workflows/session-persistence.yml`
- Modify: `docs/SESSION-PERSISTENCE-SPEC.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the `unit-xplat` job (no test to write — CI config)**

In `.github/workflows/session-persistence.yml`, add a third job. Add the test path to the `paths:` trigger list first:

```yaml
      - 'scripts/verify-session-persistence_test.go'
```

Then append the job:

```yaml
  unit-xplat:
    name: harness unit tests (${{ matrix.os }})
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - name: Checkout
        uses: actions/checkout@v6

      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version-file: go.mod

      - name: Ensure jq present
        # Only jq is needed: the unit tests stub the tmux-calling helper and
        # never invoke real tmux. (Don't install tmux here — wasted brew time.)
        shell: bash
        run: |
          if [[ "$RUNNER_OS" == "macOS" ]]; then
            command -v jq >/dev/null || brew install jq
          else
            command -v jq >/dev/null || sudo apt-get install -y jq
          fi

      - name: go test ./scripts/...
        run: go test ./scripts/... -count=1 -v
```

- [ ] **Step 2: Verify the workflow is valid YAML / actionlint-clean**

Run: `command -v actionlint >/dev/null && actionlint .github/workflows/session-persistence.yml || echo "actionlint not installed — skipping"`
Expected: no errors (or skip notice).

- [ ] **Step 3: Record the degradation-path contract in the spec**

In `docs/SESSION-PERSISTENCE-SPEC.md`, add to the verification section a note documenting the hardened behavior:

```markdown
### Cross-platform degradation contract (v1.5.2 harness hardening)

`scripts/verify-session-persistence.sh` MUST run cleanly on non-systemd hosts:

- Scenario 2 (login-teardown) and the cgroup branch of scenario 1 `[SKIP]` on
  non-Linux — unchanged; these gate a Linux+systemd-only contract.
- Scenarios 3/4 `[SKIP]` (never `[FAIL]`) when claude argv is unobservable for
  the managed session (stub bypassed by a pre-existing shared tmux daemon, or
  empty `pane_start_command`). The harness never asserts on host-wide processes.
- Scenario 5 resolves its tmux session name via `session show --json`.
- The harness removes its own `${TMPDIR}/adeck-verify.*` tempdir on exit.

Unit-gated by `scripts/verify-session-persistence_test.go` on macOS + Linux CI.
```

- [ ] **Step 4: Add a CHANGELOG entry**

In `CHANGELOG.md`, under the Unreleased/next section, add:

```markdown
### Fixed
- `verify-session-persistence.sh` now degrades truthfully on macOS/non-systemd
  hosts: scenarios 3/4 `[SKIP]` instead of false `[FAIL]` when claude argv is
  unobservable, scenario 5 resolves its tmux name via `session show --json`, and
  the harness cleans up its own tempdir. Gated by new macOS + Linux unit tests.
```

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/session-persistence.yml docs/SESSION-PERSISTENCE-SPEC.md CHANGELOG.md
git commit -m "ci(verify-persistence): unit-test harness on macOS + Linux; doc sync"
```

---

## Task 6: Mandated verification (v1.5.2 gate)

**Files:** none (verification only)

- [ ] **Step 1: Full harness unit suite, both behaviors**

Run: `GOTOOLCHAIN=go1.25.11 go test ./scripts/... -count=1 -v`
Expected: PASS — `TestLibOnly_*`, `TestIsOwnTmproot_*`, `TestResolveTmuxSession_*`, `TestCaptureClaudeArgv_*`.

- [ ] **Step 2: Persistence regression suite (mandate)**

Run: `GOTOOLCHAIN=go1.25.11 go test -run TestPersistence_ ./internal/session/... -race -count=1`
Expected: PASS (8 `TestPersistence_*` tests — proves the script changes didn't disturb the contract).

- [ ] **Step 3: macOS end-to-end smoke (this dev host)**

Run:
```bash
go build -o /tmp/agent-deck ./cmd/agent-deck && export PATH="/tmp:$PATH"
AGENT_DECK_VERIFY_USE_STUB=1 bash scripts/verify-session-persistence.sh; echo "exit=$?"
```
Expected: `OVERALL: PASS`, `exit=0` — scenarios 1 PASS, 2 SKIP (no systemd), 3/4 PASS *or* SKIP (never FAIL), 5 PASS or SKIP. Then confirm no tempdir leak:
Run: `ls -d "${TMPDIR:-/tmp}"/adeck-verify.* 2>/dev/null && echo LEAK || echo CLEAN`
Expected: `CLEAN`.

- [ ] **Step 4: Linux+systemd end-to-end (REQUIRED by CLAUDE.md before merge)**

On a Linux+systemd host (or the `session-persistence.yml` CI run / `workflow_dispatch`):
```bash
GOTOOLCHAIN=go1.25.11 go build -o /tmp/agent-deck ./cmd/agent-deck && export PATH="/tmp:$PATH"
AGENT_DECK_VERIFY_USE_STUB=1 bash scripts/verify-session-persistence.sh; echo "exit=$?"
```
Expected: `OVERALL: PASS`, scenario 2 actually executes (not skipped) and PASSes. Capture this output (or the CI link) for the PR body — the mandate requires it.

- [ ] **Step 5: Finalize**

Use `superpowers:finishing-a-development-branch` to choose merge/PR. Do NOT `git push`, `gh pr create`, or tag without explicit user approval (CLAUDE.md). No `--no-verify` on any commit in this branch.

---

## Self-Review

**Spec coverage:** A → Task 4 (`capture_claude_argv` + `classify_argv`; the `classify_argv` empty→skip test maps to the reported FAIL symptom). B → Task 2 (`is_own_tmproot`, portable mktemp). C → Task 3 (`resolve_tmux_session`, scenario 5). Source-guard prerequisite → Task 1. Cross-platform regression prevention → Task 5 CI. Mandate gates → Task 6. All covered.

**Placeholder scan:** No TBD/“add error handling”/“similar to Task N”. Every code step shows full code.

**Type/name consistency:** Function names used consistently across tasks — `is_own_tmproot` (T2, used in `cleanup`), `resolve_tmux_session` (T3, used by `tmux_pid_for_session`, `tmux_pane_start_command_for_session`, scenario 5), `capture_claude_argv` + `classify_argv` (T4, used by scenarios 3 & 4), `main` (T1, gated by `AGENT_DECK_VERIFY_LIB_ONLY`). Go helpers `sourceAndRun`/`scriptPath`/`writeFakeAgentDeck` defined once (T1/T3) and reused. Env seam `ARGV_OUT` matches the script global.

**Verified assumptions:** A test-only `scripts` package (`*_test.go` with no non-test `.go`) compiles and is picked up by both `go test ./scripts/` and `go test ./...` — confirmed empirically in this repo before handoff. `${p##*/}` and the explicit-template `mktemp` form both behave correctly on macOS bash 3.2. This repo has no `PROJECTS.md` trunk, so the global PROJECTS.md process does not apply.

**Bash 3.2 / `set -e` caveats honored:** no associative arrays; test snippets gate non-zero returns with `if`/`||` so the harness's `set -e` doesn't abort them.
