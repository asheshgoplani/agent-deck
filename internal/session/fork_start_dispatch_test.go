package session

import (
	"reflect"
	"regexp"
	"strings"
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

func TestCodexForkStartDispatchConsumesAwaitingStart(t *testing.T) {
	requireCodexForkStartGuard(t, "Start")
	requireCodexForkStartGuard(t, "StartWithMessage")
}

func requireCodexForkStartGuard(t *testing.T, funcName string) {
	t.Helper()
	body := extractFuncBodyInstance(funcName)
	if body == "" {
		t.Fatalf("%s body not found", funcName)
	}
	codexIdx := strings.Index(body, "case IsCodexCompatible(i.Tool):")
	if codexIdx < 0 {
		t.Fatalf("%s must have a Codex-compatible dispatch branch", funcName)
	}
	codexBody := body[codexIdx:]
	nextCase := strings.Index(codexBody[len("case IsCodexCompatible(i.Tool):"):], "\n\tcase ")
	if nextCase >= 0 {
		codexBody = codexBody[:len("case IsCodexCompatible(i.Tool):")+nextCase]
	}
	sentinelIdx := strings.Index(codexBody, "if i.IsForkAwaitingStart")
	buildIdx := strings.Index(codexBody, "i.buildCodexCommand")
	if sentinelIdx < 0 {
		t.Fatalf("%s Codex branch must consume IsForkAwaitingStart before normal resume/fresh dispatch", funcName)
	}
	if buildIdx < 0 {
		t.Fatalf("%s Codex branch must still call buildCodexCommand for normal starts", funcName)
	}
	if sentinelIdx > buildIdx {
		t.Fatalf("%s Codex branch must check IsForkAwaitingStart before buildCodexCommand", funcName)
	}
}

// TestCodexForkStartStampsStartedAt guards a PR #1299 review finding (CodeRabbit):
// the Codex fork-awaiting-start branch `break`s before the normal
// `i.CodexStartedAt = time.Now()` stamp, so a forked Codex child starts with no
// start-time bound. If live-process detection misses, the fallback disk scan can
// rebind the child to an older same-project rollout — including the parent it
// just forked from — propagating the wrong codex_session_id. The fork-first-start
// branch must therefore stamp CodexStartedAt itself, in both Start() and
// StartWithMessage().
func TestCodexForkStartStampsStartedAt(t *testing.T) {
	requireCodexForkAwaitingStartStampsStartedAt(t, "Start")
	requireCodexForkAwaitingStartStampsStartedAt(t, "StartWithMessage")
}

func requireCodexForkAwaitingStartStampsStartedAt(t *testing.T, funcName string) {
	t.Helper()
	body := extractFuncBodyInstance(funcName)
	if body == "" {
		t.Fatalf("%s body not found", funcName)
	}
	codexIdx := strings.Index(body, "case IsCodexCompatible(i.Tool):")
	if codexIdx < 0 {
		t.Fatalf("%s must have a Codex-compatible dispatch branch", funcName)
	}
	codexBody := body[codexIdx:]
	if nextCase := strings.Index(codexBody[len("case IsCodexCompatible(i.Tool):"):], "\n\tcase "); nextCase >= 0 {
		codexBody = codexBody[:len("case IsCodexCompatible(i.Tool):")+nextCase]
	}
	awaitingIdx := strings.Index(codexBody, "if i.IsForkAwaitingStart")
	if awaitingIdx < 0 {
		t.Fatalf("%s Codex branch must have a fork-awaiting-start block", funcName)
	}
	// Scope to the fork-awaiting-start block: from the `if` to its `break`
	// statement. Match the statement (newline + indent + break), not the bare
	// word, so a comment containing "break(s)" can't truncate the block early.
	awaitingBlock := codexBody[awaitingIdx:]
	if breakIdx := strings.Index(awaitingBlock, "\n\t\t\tbreak"); breakIdx >= 0 {
		awaitingBlock = awaitingBlock[:breakIdx]
	}
	if !strings.Contains(awaitingBlock, "i.CodexStartedAt = ") {
		t.Fatalf("%s Codex fork-awaiting-start branch MUST stamp i.CodexStartedAt (else the disk-scan fallback can rebind the fork to the parent's older rollout)", funcName)
	}
}
