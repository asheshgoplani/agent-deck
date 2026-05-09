package sessionstatus_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionstatus"
)

// fixedNow is the anchor used for every test in this file. Tests express hook
// freshness as offsets from this anchor so the freshness arithmetic is
// deterministic and not subject to wall-clock drift.
var fixedNow = time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

// TestDerive_StoppedNeverOverridden encodes the user-intentional rule shared by
// every surface: a stopped session is stopped no matter what the hook says.
// This is the only branch that runs before the tool gate, so the test seeds a
// claude tool with a fresh waiting hook to make sure the override is suppressed.
func TestDerive_StoppedNeverOverridden(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusStopped,
		Hook: &session.HookStatus{
			Status:    "waiting",
			UpdatedAt: fixedNow.Add(-10 * time.Second),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusStopped {
		t.Fatalf("stopped must be sticky against hook overrides: got %q", out.Status)
	}
	if out.Applied {
		t.Fatalf("Applied must be false when hook is suppressed by stopped guard")
	}
}

// TestDerive_NonHookEmittingToolsUntouched covers the tool gate. shell/custom
// tools never had their snapshot status overlaid by hook data; this contract
// is shared by web/snapshot_hook_refresh.go and instance.go's UpdateStatus.
func TestDerive_NonHookEmittingToolsUntouched(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"shell", "custom-tool", ""} {
		t.Run(tool, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        tool,
				PriorStatus: session.StatusError,
				Hook: &session.HookStatus{
					Status:    "waiting",
					UpdatedAt: fixedNow.Add(-10 * time.Second),
				},
				Now: fixedNow,
			})
			if out.Status != session.StatusError {
				t.Fatalf("non-hook tool %q must keep prior status: got %q", tool, out.Status)
			}
			if out.Applied {
				t.Fatalf("Applied must be false when tool gate suppresses overlay")
			}
		})
	}
}

// TestDerive_FreshHookOverridesStaleSnapshotError reproduces the v1.7.81
// production bug for the web surface: snapshot=error, fresh hook=waiting.
// Both surfaces (web and instance.go) must lift the snapshot to waiting.
func TestDerive_FreshHookOverridesStaleSnapshotError(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"claude", "codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        tool,
				PriorStatus: session.StatusError,
				Hook: &session.HookStatus{
					Status:    "waiting",
					Event:     "Stop",
					UpdatedAt: fixedNow.Add(-30 * time.Second),
				},
				Now: fixedNow,
			})
			if out.Status != session.StatusWaiting {
				t.Fatalf("fresh waiting hook must override snapshot=error: got %q", out.Status)
			}
		})
	}
}

// TestDerive_StaleRunningDoesNotOverride asserts the asymmetry that has been
// in place since v1.8.0: stale running is transient and must not override.
// Holds for both surface modes (AllowStaleWaiting affects only "waiting").
func TestDerive_StaleRunningDoesNotOverride(t *testing.T) {
	t.Parallel()
	for _, allowStaleWaiting := range []bool{true, false} {
		name := "instance-mode"
		if allowStaleWaiting {
			name = "web-mode"
		}
		t.Run(name, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        "claude",
				PriorStatus: session.StatusError,
				Hook: &session.HookStatus{
					Status:    "running",
					UpdatedAt: fixedNow.Add(-30 * time.Minute),
				},
				Now:               fixedNow,
				AllowStaleWaiting: allowStaleWaiting,
			})
			if out.Status != session.StatusError {
				t.Fatalf("stale running must NOT override (transient): got %q", out.Status)
			}
		})
	}
}

// TestDerive_AllowStaleWaiting_OverridesNonStopped pins the web read-path
// asymmetry: when the surface has no tmux fallback, a "waiting" hook is
// durable and overrides any non-stopped snapshot status, even if old. This
// preserves the v1.8.0 #867 behavior currently in snapshot_hook_refresh.go.
func TestDerive_AllowStaleWaiting_OverridesNonStopped(t *testing.T) {
	t.Parallel()
	for _, snapshotState := range []session.Status{
		session.StatusRunning, session.StatusError, session.StatusIdle, session.StatusStarting,
	} {
		t.Run(string(snapshotState), func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        "claude",
				PriorStatus: snapshotState,
				Hook: &session.HookStatus{
					Status:    "waiting",
					UpdatedAt: fixedNow.Add(-2*time.Hour - time.Second),
				},
				Now:               fixedNow,
				AllowStaleWaiting: true,
			})
			if out.Status != session.StatusWaiting {
				t.Fatalf("snapshot=%q + hook=waiting (web mode) must yield waiting: got %q", snapshotState, out.Status)
			}
		})
	}
}

// TestDerive_AllowStaleWaiting_False_StaleHookFallsThrough is the parity-bug
// proof: in instance-mode (no AllowStaleWaiting), a stale hook must fall
// through unchanged so the caller's tmux fallback can run. The current
// snapshot_hook_refresh.go behavior of always overriding on "waiting" is
// surface-specific, not universal.
func TestDerive_AllowStaleWaiting_False_StaleHookFallsThrough(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusError,
		Hook: &session.HookStatus{
			Status:    "waiting",
			UpdatedAt: fixedNow.Add(-30 * time.Minute),
		},
		Now:               fixedNow,
		AllowStaleWaiting: false,
	})
	if out.Status != session.StatusError {
		t.Fatalf("instance-mode stale waiting must fall through to tmux fallback: got %q", out.Status)
	}
	if out.Applied {
		t.Fatalf("Applied must be false when stale hook does not override")
	}
}

// TestDerive_CodexRunning20sWindow is the codex-specific parity bug: the
// snapshot_hook_refresh.go path uses a single 2-minute freshness window for
// every "running" hook, but instance.go uses a 20-second window for codex
// "running". This test pins the 20s contract in the shared helper so both
// surfaces converge.
func TestDerive_CodexRunning20sWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		hookAge time.Duration
		wantSt  session.Status
		applied bool
	}{
		{"fresh-19s", 19 * time.Second, session.StatusRunning, true},
		{"stale-21s", 21 * time.Second, session.StatusIdle, false},
		{"stale-1m", 1 * time.Minute, session.StatusIdle, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        "codex",
				PriorStatus: session.StatusIdle,
				Hook: &session.HookStatus{
					Status:    "running",
					UpdatedAt: fixedNow.Add(-tt.hookAge),
				},
				Now: fixedNow,
			})
			if out.Status != tt.wantSt {
				t.Fatalf("codex running age=%v: got %q want %q", tt.hookAge, out.Status, tt.wantSt)
			}
			if out.Applied != tt.applied {
				t.Fatalf("codex running age=%v: Applied got %v want %v", tt.hookAge, out.Applied, tt.applied)
			}
		})
	}
}

// TestDerive_CodexWaiting2mWindow asserts codex "waiting" uses the longer
// window. instance.go has used 2-minutes for codex waiting since v1.7.x; the
// shared helper must preserve that to avoid a regression in codex
// attention-needed signaling.
func TestDerive_CodexWaiting2mWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		hookAge time.Duration
		want    session.Status
	}{
		{"fresh-1m", 1 * time.Minute, session.StatusWaiting},
		{"fresh-119s", 119 * time.Second, session.StatusWaiting},
		// instance-mode (no AllowStaleWaiting): 2m1s is stale → falls through.
		{"stale-2m1s", 2*time.Minute + time.Second, session.StatusIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        "codex",
				PriorStatus: session.StatusIdle,
				Hook: &session.HookStatus{
					Status:    "waiting",
					UpdatedAt: fixedNow.Add(-tt.hookAge),
				},
				Now: fixedNow,
			})
			if out.Status != tt.want {
				t.Fatalf("codex waiting age=%v: got %q want %q", tt.hookAge, out.Status, tt.want)
			}
		})
	}
}

// TestDerive_ClaudeWaiting_AcknowledgedYieldsIdle is divergence #3 from the
// audit: Instance.UpdateStatus drops a claude "waiting" hook to StatusIdle
// when the user has attached (Acknowledged), while snapshot_hook_refresh.go
// always lifts to StatusWaiting. The web DTO doesn't carry the acknowledged
// bit today, so the caller passes Acknowledged=false in v1.9.0; the
// contract here is that Derive HONORS the bit when set, unlocking a future
// migration where the DTO learns the field.
func TestDerive_ClaudeWaiting_AcknowledgedYieldsIdle(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:         "claude",
		PriorStatus:  session.StatusRunning,
		Acknowledged: true,
		Hook: &session.HookStatus{
			Status:    "waiting",
			UpdatedAt: fixedNow.Add(-10 * time.Second),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusIdle {
		t.Fatalf("claude waiting + acknowledged must yield idle (matches instance.go): got %q", out.Status)
	}
	if !out.Applied {
		t.Fatalf("Applied must be true when hook drives the idle transition")
	}
}

// TestDerive_CodexWaiting_AcknowledgedStillWaiting locks the codex-specific
// rule from instance.go:2886-2893: codex completion surfaces as
// attention-needed (StatusWaiting) regardless of acknowledged state. Without
// this assertion the helper could be naively factored to "waiting +
// acknowledged → idle" and silently regress codex parity.
func TestDerive_CodexWaiting_AcknowledgedStillWaiting(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:         "codex",
		PriorStatus:  session.StatusRunning,
		Acknowledged: true,
		Hook: &session.HookStatus{
			Status:    "waiting",
			UpdatedAt: fixedNow.Add(-10 * time.Second),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusWaiting {
		t.Fatalf("codex waiting must surface as attention-needed even when acknowledged: got %q", out.Status)
	}
}

// TestDerive_HookDead_FreshOverrides asserts dead-hook fast path. instance.go
// uses the 2-minute window for non-codex; codex follows the running window
// (20s) when applying "dead" because it is also lifecycle-transient. The
// helper picks the conservative claude/gemini default of 2m for "dead"
// across all tools — that matches the existing snapshot_hook_refresh.go
// behavior and is what TestRefreshSnapshotHookStatuses tests already exercise.
func TestDerive_HookDead_FreshOverrides(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusRunning,
		Hook: &session.HookStatus{
			Status:    "dead",
			UpdatedAt: fixedNow.Add(-30 * time.Second),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusError {
		t.Fatalf("fresh dead hook must yield error: got %q", out.Status)
	}
}

// TestDerive_HookDead_StaleDoesNotOverride matches the conservative
// asymmetry already encoded in snapshot_hook_refresh.go (only fresh dead
// overrides). Stale dead is risky — the session may have been restarted.
func TestDerive_HookDead_StaleDoesNotOverride(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusRunning,
		Hook: &session.HookStatus{
			Status:    "dead",
			UpdatedAt: fixedNow.Add(-30 * time.Minute),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusRunning {
		t.Fatalf("stale dead must not override: got %q", out.Status)
	}
}

// TestDerive_NoHookPassesThrough is the property test the master plan
// requested: any value of session.Status round-trips when no hook is
// supplied. Locks the "snapshot is otherwise passed through" contract.
func TestDerive_NoHookPassesThrough(t *testing.T) {
	t.Parallel()
	for _, st := range []session.Status{
		session.StatusRunning, session.StatusWaiting, session.StatusIdle,
		session.StatusError, session.StatusStarting, session.StatusStopped,
	} {
		t.Run(string(st), func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        "claude",
				PriorStatus: st,
				Hook:        nil,
				Now:         fixedNow,
			})
			if out.Status != st {
				t.Fatalf("status %q changed to %q with no hook overlay", st, out.Status)
			}
			if out.Applied {
				t.Fatalf("Applied must be false when no hook is supplied")
			}
		})
	}
}

// TestDerive_HookWithoutTimestamp pins the "missing UpdatedAt" branch:
// instance.go and snapshot_hook_refresh.go both treat zero-UpdatedAt hooks
// differently today. snapshot_hook_refresh skips them entirely; instance.go
// computes time.Since(zeroTime) ≫ window and falls through. The shared
// helper must agree — we choose the snapshot_hook_refresh behavior (skip)
// because zero UpdatedAt represents a malformed file, not a fresh signal.
func TestDerive_HookWithoutTimestamp(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusError,
		Hook: &session.HookStatus{
			Status:    "running",
			UpdatedAt: time.Time{},
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusError {
		t.Fatalf("hook with zero UpdatedAt must not override: got %q", out.Status)
	}
	if out.Applied {
		t.Fatalf("Applied must be false for malformed hook (zero UpdatedAt)")
	}
}

// TestDerive_UnknownHookStatusFallsThrough covers the long-tail: a hook with
// an unrecognized status string (e.g. a future "compacting" event the helper
// has not learned yet) must not corrupt the prior status. This locks the
// "fail open" contract — adding a new hook state cannot regress existing
// surfaces silently.
func TestDerive_UnknownHookStatusFallsThrough(t *testing.T) {
	t.Parallel()
	out := sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusRunning,
		Hook: &session.HookStatus{
			Status:    "compacting",
			UpdatedAt: fixedNow.Add(-10 * time.Second),
		},
		Now: fixedNow,
	})
	if out.Status != session.StatusRunning {
		t.Fatalf("unknown hook status must fall through: got %q", out.Status)
	}
	if out.Applied {
		t.Fatalf("Applied must be false on unknown hook status")
	}
}

// TestDerive_PureFunction_NoMutation asserts Derive does not mutate its
// inputs. Surfaces share *HookStatus pointers via maps; a hidden mutation
// would be a serious concurrency hazard.
func TestDerive_PureFunction_NoMutation(t *testing.T) {
	t.Parallel()
	hook := &session.HookStatus{
		Status:    "waiting",
		Event:     "Stop",
		SessionID: "claude-abc",
		UpdatedAt: fixedNow.Add(-30 * time.Second),
	}
	original := *hook
	_ = sessionstatus.Derive(sessionstatus.Input{
		Tool:        "claude",
		PriorStatus: session.StatusError,
		Hook:        hook,
		Now:         fixedNow,
	})
	if *hook != original {
		t.Fatalf("Derive mutated hook: got %+v want %+v", *hook, original)
	}
}

// TestDerive_ToolGate_CodexAndGemini explicitly asserts that codex and
// gemini are inside the hook-emitting set. instance.go gates with
// `IsClaudeCompatible(i.Tool) || i.Tool == "codex" || i.Tool == "gemini"`;
// the helper must mirror exactly to avoid a third site drifting.
func TestDerive_ToolGate_CodexAndGemini(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			out := sessionstatus.Derive(sessionstatus.Input{
				Tool:        tool,
				PriorStatus: session.StatusError,
				Hook: &session.HookStatus{
					Status:    "running",
					UpdatedAt: fixedNow.Add(-10 * time.Second),
				},
				Now: fixedNow,
			})
			if out.Status != session.StatusRunning {
				t.Fatalf("tool %q must be hook-emitting: got %q", tool, out.Status)
			}
		})
	}
}
