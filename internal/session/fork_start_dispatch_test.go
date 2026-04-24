package session

import (
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRegression745_ForkTargetCarriesAwaitingStartSentinel guards #745.
//
// @petitcl reported that sessions forked via f/F in the TUI come up empty
// — the new session has none of the conversation history from the parent.
//
// Root cause: Instance.Start()'s claude-compatible dispatch
// (instance.go:2173-2183) rebuilds the command unconditionally:
//
//	if i.ClaudeSessionID != "" {
//	    command = i.buildClaudeResumeCommand()
//	} else {
//	    command = i.buildClaudeCommand(i.Command)
//	}
//
// For a fork target, buildClaudeForkCommandForTarget pre-populates
// i.ClaudeSessionID with the new fork UUID and stashes the real fork
// command (containing --resume <parent-id> --fork-session) in i.Command.
// Start() then enters the `ClaudeSessionID != ""` branch and runs
// buildClaudeResumeCommand — which, seeing no JSONL for the brand-new
// fork UUID, falls back to a plain --session-id <forkUUID> with NO
// --resume and NO --fork-session. The forked session starts fresh with
// no context — exactly the reported symptom.
//
// Fix: add a transient IsForkAwaitingStart sentinel (not persisted).
// The fork builder sets it; Start() consumes it as the FIRST check in
// the claude-compatible branch and uses i.Command directly, bypassing
// buildClaudeResumeCommand/buildClaudeCommand.
//
// This test uses reflection so a missing field surfaces as a clean
// assertion failure rather than a compile error. It also structurally
// verifies Start() actually honors the sentinel before the
// buildClaudeResumeCommand call site, since the transient flag is
// useless without the consuming check.
func TestRegression745_ForkTargetCarriesAwaitingStartSentinel(t *testing.T) {
	parent := NewInstanceWithTool("parent", "/tmp", "claude")
	parent.ClaudeSessionID = "parent-abc-123"
	parent.ClaudeDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedInstance("forked", "")
	require.NoError(t, err, "CreateForkedInstance should succeed")

	// Contract 1: the fork command builder embeds --fork-session and
	// --resume <parent-id>. (Existing invariant — guards against the fork
	// builder itself regressing.)
	require.Contains(t, cmd, "--fork-session",
		"fork command MUST include --fork-session")
	require.Contains(t, cmd, "--resume parent-abc-123",
		"fork command MUST include --resume <parent-id>")

	// Contract 2: the fork target carries a transient sentinel so
	// Start() bypasses the claude-compatible resume/fresh dispatch and
	// uses i.Command directly.
	sentinel, hasField := forkAwaitingStartValue(forked)
	require.True(t, hasField,
		"Instance MUST declare a bool field IsForkAwaitingStart (#745)")
	require.True(t, sentinel,
		"CreateForkedInstanceWithOptions MUST set IsForkAwaitingStart=true on the fork target (#745)")

	// Contract 3: the field carries json:"-" (transient — a persisted
	// fork-awaiting flag would cause a restart of the forked session to
	// re-emit --fork-session, double-counting the parent conversation).
	tag, hasField := forkAwaitingStartTag(forked)
	require.True(t, hasField, "Instance.IsForkAwaitingStart field must exist")
	require.Equal(t, "-", tag,
		"Instance.IsForkAwaitingStart MUST be tagged json:\"-\" — transient only")

	// Contract 4: Start()'s claude-compatible dispatch MUST consult
	// IsForkAwaitingStart BEFORE calling buildClaudeResumeCommand. Without
	// this early return, the sentinel is inert and the #745 symptom
	// survives. Structural grep asserted against instance.go so this
	// cannot regress silently in a future refactor.
	require.True(t, startDispatchHonorsForkSentinel(),
		"Instance.Start() MUST consult IsForkAwaitingStart before invoking buildClaudeResumeCommand / buildClaudeCommand (#745)")
}

// forkAwaitingStartValue returns (value, true) when the Instance struct
// has a bool field named IsForkAwaitingStart; (false, false) otherwise.
func forkAwaitingStartValue(i *Instance) (bool, bool) {
	v := reflect.ValueOf(i).Elem()
	f := v.FieldByName("IsForkAwaitingStart")
	if !f.IsValid() || f.Kind() != reflect.Bool {
		return false, false
	}
	return f.Bool(), true
}

// forkAwaitingStartTag returns (json-tag, true) for the field's `json`
// struct tag, or ("", false) if the field doesn't exist.
func forkAwaitingStartTag(i *Instance) (string, bool) {
	typ := reflect.TypeOf(i).Elem()
	f, ok := typ.FieldByName("IsForkAwaitingStart")
	if !ok {
		return "", false
	}
	return f.Tag.Get("json"), true
}

// startDispatchHonorsForkSentinel structurally asserts that
// Instance.Start() checks IsForkAwaitingStart before invoking the
// claude-compatible resume/fresh dispatch. Pattern required:
//
//	if i.IsForkAwaitingStart { ... command = i.Command ... }
//
// ... appearing BEFORE buildClaudeResumeCommand(i) inside Start().
func startDispatchHonorsForkSentinel() bool {
	body := extractFuncBodyInstance("Start")
	if body == "" {
		return false
	}
	// Require an early return / direct-use branch on IsForkAwaitingStart.
	earlyRe := regexp.MustCompile(`i\.IsForkAwaitingStart`)
	resumeRe := regexp.MustCompile(`i\.buildClaudeResumeCommand\(`)
	earlyIdx := earlyRe.FindStringIndex(body)
	if earlyIdx == nil {
		return false
	}
	resumeIdx := resumeRe.FindStringIndex(body)
	if resumeIdx == nil {
		return false
	}
	// Sentinel-use must precede the resume call in source order.
	return earlyIdx[0] < resumeIdx[0]
}
