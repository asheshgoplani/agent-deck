// Package sessionstatus is the single owner of the hook→status derivation
// logic shared by every surface that maps a session.HookStatus payload onto a
// session.Status (CLI cold-load, web read-path, TUI inotify watcher, and the
// transition daemon).
//
// Before this package, the same decision tree lived in three places:
//   - internal/session/instance.go (UpdateStatus's hook fast-path block)
//   - internal/web/snapshot_hook_refresh.go (applyHookStatusToMenuSession)
//   - the CLI cold-load path that funnels into UpdateStatus
//
// The duplicates drifted: codex's 20-second freshness for "running", the
// claude-acknowledged → idle transition, and the tool gate were all encoded
// inconsistently across surfaces, producing the parity bugs that V1.9
// PRIORITY plan calls out as theme T1.
//
// This package owns ONLY the hook→status mapping. The tmux pane-title
// fallback that lives downstream of the hook fast path in
// Instance.UpdateStatus is intentionally left where it is; extracting it
// requires the full event-bus rewrite which is v2.0 scope (master plan
// risk #1: "start by extracting *only* the hook→status mapping").
//
// Surfaces with a tmux fallback (CLI/TUI via Instance.UpdateStatus) call
// Derive with AllowStaleWaiting=false: stale "waiting" hooks fall through
// so the fallback can run. The web read-path has no per-request tmux
// budget, so it calls Derive with AllowStaleWaiting=true: a stale waiting
// hook is treated as a durable proxy for the tmux signal it would have
// observed (preserving the v1.8.0 #867 behavior that fixes the web
// stale-error class).
package sessionstatus

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Freshness windows. Mirror the constants in internal/session/instance.go
// (intentionally duplicated rather than re-exported to keep session/ free
// of an upward dependency on this package — see package doc).
const (
	HookFastPathWindow             = 2 * time.Minute
	CodexHookRunningFastPathWindow = 20 * time.Second
	CodexHookWaitingFastPathWindow = 2 * time.Minute
)

// Input is the value-typed input to Derive. Surfaces construct it from
// whatever wire data they have (snapshot DTO, instance fields, hook file).
type Input struct {
	// Tool is the session's tool ID (e.g. "claude", "codex", "gemini",
	// "shell"). Tools outside the hook-emitting set leave PriorStatus
	// untouched.
	Tool string

	// PriorStatus is the surface's current best-guess status before the
	// hook overlay runs. For the web read-path this is the snapshot's
	// MenuSession.Status; for instance.go this is i.Status.
	PriorStatus session.Status

	// Hook is the latest hook payload for this instance, or nil if none
	// is available. A non-nil Hook with empty Status or zero UpdatedAt is
	// treated as malformed and ignored.
	Hook *session.HookStatus

	// Acknowledged signals that the user has attached to the session
	// since the last "waiting" event. When true and the tool is
	// claude-compatible/gemini, a fresh "waiting" hook resolves to
	// StatusIdle instead of StatusWaiting (matches Instance.UpdateStatus
	// at instance.go:2899). Codex ignores this bit by design.
	Acknowledged bool

	// Now is injected for deterministic freshness arithmetic. Tests pass
	// a fixed value; production passes time.Now().
	Now time.Time

	// AllowStaleWaiting toggles the surface asymmetry: when true, a
	// "waiting" hook overrides any non-stopped PriorStatus regardless of
	// freshness. When false, stale hooks fall through. See package doc.
	AllowStaleWaiting bool
}

// Decision is the result of Derive. Status is the post-hook visible status;
// Applied indicates whether the hook overlay actually changed something
// (useful for diagnostic logging — V1.9 PRIORITY theme T3).
type Decision struct {
	Status  session.Status
	Applied bool
}

// IsHookEmittingTool returns true for tools that emit lifecycle hook files.
// Mirrors the gate at internal/session/instance.go:2854 + 2873.
func IsHookEmittingTool(tool string) bool {
	if session.IsClaudeCompatible(tool) {
		return true
	}
	return tool == "codex" || tool == "gemini"
}

// freshnessFor returns the freshness window for a (tool, hookStatus) pair.
// Mirrors hookFastPathFreshnessForTool in instance.go:2767.
func freshnessFor(tool, hookStatus string) time.Duration {
	if !session.IsCodexCompatible(tool) {
		return HookFastPathWindow
	}
	switch hookStatus {
	case "waiting":
		return CodexHookWaitingFastPathWindow
	default:
		return CodexHookRunningFastPathWindow
	}
}

// Derive maps (tool, prior status, hook payload, acknowledged, clock) onto a
// final Decision. See package doc for the full contract.
func Derive(in Input) Decision {
	keep := Decision{Status: in.PriorStatus, Applied: false}

	// Stopped is user-intentional. Highest precedence; suppresses every
	// hook overlay.
	if in.PriorStatus == session.StatusStopped {
		return keep
	}

	if !IsHookEmittingTool(in.Tool) {
		return keep
	}
	if in.Hook == nil || in.Hook.Status == "" || in.Hook.UpdatedAt.IsZero() {
		return keep
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	fresh := now.Sub(in.Hook.UpdatedAt) <= freshnessFor(in.Tool, in.Hook.Status)

	switch in.Hook.Status {
	case "running":
		if !fresh {
			return keep
		}
		return Decision{Status: session.StatusRunning, Applied: true}

	case "waiting":
		// Acknowledged + claude/gemini → idle. Codex always surfaces
		// waiting because completion is attention-needed.
		if !fresh && !in.AllowStaleWaiting {
			return keep
		}
		if in.Acknowledged && !session.IsCodexCompatible(in.Tool) {
			return Decision{Status: session.StatusIdle, Applied: true}
		}
		return Decision{Status: session.StatusWaiting, Applied: true}

	case "dead":
		if !fresh {
			return keep
		}
		return Decision{Status: session.StatusError, Applied: true}
	}
	return keep
}
