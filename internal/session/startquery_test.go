package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// setStartupQueryField uses reflection so this test file compiles on a tree
// where Instance.StartupQuery does not yet exist. Mirrors setExtraArgsField
// in extraargs_test.go:35.
func setStartupQueryField(t *testing.T, inst *Instance, query string) {
	t.Helper()
	val := reflect.ValueOf(inst).Elem()
	field := val.FieldByName("StartupQuery")
	if !field.IsValid() {
		t.Fatalf(
			"Instance.StartupQuery field does not exist; required for " +
				"per-session claude startup-query wiring (v1.7.67). Add " +
				"`StartupQuery string `json:\"-\"`` to the Instance struct " +
				"in internal/session/instance.go. The `json:\"-\"` tag is " +
				"mandatory — the query must NOT persist across restarts.",
		)
	}
	if field.Kind() != reflect.String {
		t.Fatalf(
			"Instance.StartupQuery has wrong kind %s; want string",
			field.Kind().String(),
		)
	}
	field.SetString(query)
}

// TestStartCommandAppendsStartupQueryAsSingleArg asserts that a
// StartupQuery containing spaces is emitted as ONE shell-quoted token on
// the claude new-session command line, NOT split on whitespace. This is
// the core v1.7.67 contract: claude-code accepts one positional query arg,
// so multi-word queries must survive the bash -c wrapper intact.
func TestStartCommandAppendsStartupQueryAsSingleArg(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("sq-single-arg", t.TempDir(), "claude")
	setStartupQueryField(t, inst, "explain the codebase")

	cmd := inst.buildClaudeCommand("claude")

	// The multi-word query must be emitted as a single shell token.
	// Accept either single-quoted or double-quoted forms (shellescape
	// produces single quotes). If it appears raw, bash -c will split it
	// into three positional args ("explain", "the", "codebase") and
	// claude will receive garbage.
	hasQuoted := strings.Contains(cmd, `'explain the codebase'`) ||
		strings.Contains(cmd, `"explain the codebase"`)
	if !hasQuoted {
		t.Fatalf(
			"StartupQuery with spaces was not shell-quoted as a single "+
				"argv element; bash -c will re-split it. Use "+
				"shellescape.Quote(i.StartupQuery) when appending. got:\n%s",
			cmd,
		)
	}
}

// TestStartCommandOmitsStartupQueryWhenEmpty asserts no stray tokens or
// empty quotes appear when StartupQuery is empty. Prevents the regression
// where an empty field emits `”` as a positional arg (claude would treat
// it as an empty prompt and block waiting for input).
func TestStartCommandOmitsStartupQueryWhenEmpty(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("sq-empty", t.TempDir(), "claude")
	// Probe the field exists but leave it at zero value.
	val := reflect.ValueOf(inst).Elem()
	if !val.FieldByName("StartupQuery").IsValid() {
		t.Fatalf("Instance.StartupQuery field does not exist")
	}

	cmd := inst.buildClaudeCommand("claude")

	if strings.Contains(cmd, " ''") || strings.Contains(cmd, ` ""`) {
		t.Errorf(
			"empty StartupQuery produced stray empty-quoted arg in "+
				"command, got:\n%s",
			cmd,
		)
	}
	if strings.Contains(cmd, "  ") {
		t.Errorf("empty StartupQuery produced double-space in command, got:\n%s", cmd)
	}
}

// TestStartupQueryDoesNotPersistToJSON asserts the `json:"-"` tag holds:
// StartupQuery is a per-launch transient and must not land in SQLite. If
// this test fails, sessions will replay the query on every restart —
// exactly the Clindbergh bug (GH #725) the feature is meant to fix.
func TestStartupQueryDoesNotPersistToJSON(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("sq-no-persist", t.TempDir(), "claude")
	setStartupQueryField(t, inst, "please do the thing")

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}

	// Check for field-shaped leaks only; the test name in t.TempDir()
	// incidentally contains "StartupQuery" as a substring (via group_path).
	leaks := []string{
		`"startup_query":`,
		`"StartupQuery":`,
		`please do the thing`,
	}
	for _, needle := range leaks {
		if strings.Contains(string(data), needle) {
			t.Fatalf(
				"StartupQuery leaked into JSON (found %q); must be tagged "+
					"`json:\"-\"` so it does not survive restarts. "+
					"marshalled:\n%s",
				needle, string(data),
			)
		}
	}
}

// TestResumeCommandOmitsStartupQuery asserts the resume path does NOT
// append StartupQuery. The query is a per-session starter; replaying it
// on restart would cause the bug the feature is meant to fix (@Clindbergh,
// GH #725). Mirrors TestResumeCommandAppendsExtraArgs at
// extraargs_test.go:143, but inverted — extra-args MUST survive resume,
// startup-query MUST NOT.
func TestResumeCommandOmitsStartupQuery(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("sq-no-resume", t.TempDir(), "claude")
	inst.ClaudeSessionID = "00000000-0000-0000-0000-000000000000"
	setStartupQueryField(t, inst, "explain the codebase")

	cmd := inst.buildClaudeResumeCommand()

	if strings.Contains(cmd, "explain the codebase") {
		t.Fatalf(
			"StartupQuery leaked into resume command; per-session only "+
				"contract violated. got:\n%s",
			cmd,
		)
	}
}

// TestStartupQueryCoexistsWithExtraArgs is the extra-args regression
// guard. Asserts that when BOTH StartupQuery and ExtraArgs are set:
//
//  1. ExtraArgs tokens are emitted as separate flags (unchanged behavior).
//  2. StartupQuery is emitted as one shell-quoted positional arg.
//  3. The two features do not interfere.
//
// This is the test the scope calls out explicitly: "extra-args
// regression test".
func TestStartupQueryCoexistsWithExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("sq-and-ea", t.TempDir(), "claude")
	setExtraArgsField(t, inst, []string{"--agent", "reviewer"})
	setStartupQueryField(t, inst, "start here please")

	cmd := inst.buildClaudeCommand("claude")

	if !strings.Contains(cmd, "--agent") || !strings.Contains(cmd, "reviewer") {
		t.Errorf("ExtraArgs dropped when StartupQuery also set, got:\n%s", cmd)
	}
	hasQuotedQuery := strings.Contains(cmd, `'start here please'`) ||
		strings.Contains(cmd, `"start here please"`)
	if !hasQuotedQuery {
		t.Errorf(
			"StartupQuery not shell-quoted when ExtraArgs also set, got:\n%s",
			cmd,
		)
	}
}
