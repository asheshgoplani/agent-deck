package genui

import (
	"context"
	"strings"
)

// compose_stub.go — the DETERMINISTIC composer for CI, TDD, and the drivable
// demo. It maps a handful of canned intents to hand-authored specs with ZERO
// LLM involvement, so the entire intent→validate→repair→render loop is testable
// and demoable without any API key or live session (the hard no-billed-call
// constraint). It is the reference producer the real SessionComposer must match
// on the wire: identical spec JSON flowing through the identical validator gate.
//
// The mapping is intentionally tiny and keyword-based — this is a stand-in for a
// model, not an NLU engine. It covers the prompt's three example intents
// ("show me what's blocked", "group everything by project", "just the
// conductors and their last action") plus a default, and ONE deliberately
// invalid intent that emits an unknown-widget spec so the validator-rejection /
// clean-error path is exercised end-to-end (the security keystone, on composed
// output).

// StubComposer is a deterministic, no-LLM SpecComposer.
type StubComposer struct{}

// Name identifies the composer in the compose trace.
func (StubComposer) Name() string { return "stub" }

// Mutates reports that the stub is pure (no side effects), so the compose
// endpoint need not gate it as a mutation — a read-only demo can compose views.
func (StubComposer) Mutates() bool { return false }

// Compose maps the intent to a canned spec by keyword. It ignores the catalog
// and data summary (it is deterministic) but honors the same ComposeRequest
// contract as a real composer. It always succeeds at the transport level; the
// returned bytes are then validated by ComposeView exactly like any composer's.
func (StubComposer) Compose(_ context.Context, req ComposeRequest) ([]byte, error) {
	return []byte(stubSpecForIntent(req.Intent)), nil
}

// stubSpecForIntent is the canned intent→spec map. Lower-cased substring match,
// most-specific first. Every returned spec is valid EXCEPT the explicit
// "invalid"/"unsafe" probe, which returns an unknown-widget spec on purpose.
func stubSpecForIntent(intent string) string {
	s := strings.ToLower(strings.TrimSpace(intent))

	switch {
	// Deliberate validator-rejection probe: proves a code/unknown-widget spec
	// from the "LLM" is caught by the unchanged validator and surfaced as a
	// clean error rather than rendered.
	case containsAny(s, "unknown widget", "unsafe", "reject", "exploit", "inject"):
		return stubSpecInvalid

	case containsAny(s, "blocked", "needs you", "need you", "waiting on", "what needs", "stuck", "decision"):
		return stubSpecBlocked

	case containsAny(s, "by project", "group", "per project", "per conductor", "grouped"):
		return stubSpecByProject

	case containsAny(s, "conductor", "last action", "each is doing", "what each", "fleet only"):
		return stubSpecConductors

	case containsAny(s, "session", "active work", "everything running", "all sessions"):
		return stubSpecSessions

	default:
		return stubSpecStatus
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// --- canned composed specs ------------------------------------------------
// These are emitted as the "LLM output". They use composed-* specIds so it is
// visible in the UI that they were generated for the intent, distinct from the
// genui-0 hand-authored views. All bind the SAME live snapshot by reference.

const stubSpecStatus = `{
  "schema": 1, "specId": "composed-status", "title": "Composed — fleet status", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "Fleet status (composed from your intent)" },
    { "type": "row", "gap": "md", "children": [
      { "type": "stat", "label": "Running", "tone": "ok",      "bind": "totals.running" },
      { "type": "stat", "label": "Waiting", "tone": "warn",    "bind": "totals.waiting" },
      { "type": "stat", "label": "Idle",    "tone": "neutral", "bind": "totals.idle" }
    ]},
    { "type": "section", "text": "The fleet", "gap": "md", "children": [
      { "type": "status-list", "bind": "conductors" }
    ]}
  ]}
}`

const stubSpecBlocked = `{
  "schema": 1, "specId": "composed-blocked", "title": "Composed — what's blocked", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "What's blocked (composed from your intent)" },
    { "type": "section", "text": "👉 Decisions waiting on you", "gap": "md", "children": [
      { "type": "decision-list", "bind": "decisionsWaiting" }
    ]},
    { "type": "section", "text": "⚠️ Stuck or errored sessions", "gap": "md", "children": [
      { "type": "session-list", "bind": "stuckSessions" }
    ]}
  ]}
}`

const stubSpecByProject = `{
  "schema": 1, "specId": "composed-by-project", "title": "Composed — grouped by project", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "By project (composed from your intent)" },
    { "type": "grid", "cols": 2, "gap": "md", "repeat": {
      "over": "conductors", "as": "item", "template": {
        "type": "section", "gap": "sm", "children": [
          { "type": "conductor-card", "bind": "item" },
          { "type": "session-list", "bind": "item.sessions" }
        ]
      }
    }}
  ]}
}`

const stubSpecConductors = `{
  "schema": 1, "specId": "composed-conductors", "title": "Composed — conductors & last action", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "Conductors and what each is doing (composed)" },
    { "type": "status-list", "bind": "conductors" }
  ]}
}`

const stubSpecSessions = `{
  "schema": 1, "specId": "composed-sessions", "title": "Composed — all active sessions", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "Every active session (composed from your intent)" },
    { "type": "session-list", "bind": "sessions" }
  ]}
}`

// stubSpecInvalid is emitted ONLY for the explicit rejection probe. It carries
// an unknown widget type so the unchanged validator rejects it — proving a bad
// "LLM" spec is caught at the gate and never reaches the renderer.
const stubSpecInvalid = `{
  "schema": 1, "specId": "composed-invalid", "title": "Composed — invalid probe", "version": 1,
  "root": { "type": "col", "gap": "lg", "children": [
    { "type": "heading", "level": 1, "text": "This should never render" },
    { "type": "iframe", "bind": "totals.running" }
  ]}
}`
