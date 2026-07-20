# Handoff unification: transcript builder + autonomous budget handoff

**Date:** 2026-07-20
**Status:** Approved design, pending implementation plan

## Problem

Two independent handoff mechanisms exist in the tree after merging `origin/main`, and
neither knows about the other.

**Origin's (`#1669`, `internal/session/handoff.go`)** builds a continuation prompt
*mechanically*: it reads the Claude JSONL transcript from disk and renders a
char-budgeted tail. It needs nothing from the running agent, so it always works —
but it produces a raw transcript tail, not a curated summary. It is wired only to
the read-only `session handoff` CLI command, and its framing is Claude→Codex specific.

**The local autonomous budget handoff (`internal/ui/context_budget_ui.go`)** builds a
continuation prompt *by asking the agent*: at `HighTokens` it tmux-messages the session
to write its own `handoff/<id>/PROMPT.md`, then forks a successor seeded from that file.
The curated prompt is higher quality, but the path depends on a responsive agent. When
`PROMPT.md` never appears — a wedged agent, or the ceiling/timeout hit first —
`forkContinuation` calls `failsafePause`: the session is killed and a human must take
over. That is a dead end in exactly the case the feature exists for.

The two are complementary: origin's builder is precisely the agent-independent fallback
the local failsafe path lacks.

## Goals

1. **Dedup** — one shared entry point for "give me the continuation prompt for this
   session", used by both the CLI command and the autonomous fork.
2. **Resilient failsafe** — a missing `PROMPT.md` no longer dead-ends; fall back to the
   mechanically-built transcript prompt.
3. **Cross-tool** — the autonomous handoff can spawn the successor with a different tool
   (e.g. Claude→Codex), not just the source's tool.

Explicit non-goal: appending the raw transcript beneath the curated prompt on every
handoff ("richer prompt"). Rejected — it duplicates what the agent already summarized
and inflates every continuation's token cost.

## Design

### 1. Shared prompt resolver (dedup)

Generalize origin's builder and add a single resolver in `internal/session/handoff.go`:

```go
// Generalized from BuildClaudeToCodexHandoffPrompt — identical transcript
// machinery, tool-parameterized framing.
func BuildContinuationHandoffPrompt(inst *Instance, targetTool string, maxChars int) (string, HandoffInfo, error)

// The single "prompt to seed a continuation with" entry point.
func ResolveContinuationPrompt(inst *Instance, targetTool, promptPath string, maxChars int) (ContinuationPrompt, error)

type ContinuationPrompt struct {
    Text   string
    Source string      // "agent" (curated PROMPT.md) | "transcript" (mechanical fallback)
    Info   HandoffInfo // transcript stats; zero when Source == "agent"
}
```

Resolution order: agent-authored `PROMPT.md` when present and non-empty (the curated,
higher-quality artifact), else the transcript tail.

`BuildClaudeToCodexHandoffPrompt` is retained as a thin wrapper passing
`targetTool: "codex"`, so origin's existing callers and tests keep passing unchanged.
This keeps section 1 additive and independently upstreamable.

Both consumers route through the resolver: `forkContinuation` (UI) and
`handleSessionHandoff` (CLI). Side effect: the CLI's `session handoff` begins honoring a
curated `PROMPT.md` when one exists, which it ignores today.

### 2. Resilient failsafe

Behavior change confined to `forkContinuation`:

| Situation | Today | After |
|---|---|---|
| `PROMPT.md` present | fork with it | fork with it (unchanged) |
| `PROMPT.md` missing/unreadable | `failsafePause` | fork with transcript-built prompt |
| Transcript also unreadable | `failsafePause` | `failsafePause` (unchanged) |

The pure state machine in `context_handoff.go` is **not** modified — `ActionFork` and
`ActionFailsafe` semantics are unchanged. Only the I/O behind `ActionFork` gains a
fallback.

The ceiling/timeout failsafe path **also** attempts a transcript-built fork before
pausing, and always raises the loud notification when it does. Rationale: the failsafe
case is where continuity matters most, and the transcript fallback does not require a
cooperative agent. The human is told unambiguously that a lower-confidence,
transcript-sourced handoff occurred.

Every transcript-sourced fork (failsafe or not) raises the loud notification — the
existing `notifyBudgetCrossing` channel already used by `failsafePause` — since
`Source == "transcript"` always indicates a degraded handoff.

### 3. Cross-tool spawn

Add `HandoffTargetTool string` to `ContextBudgetSettings`. Dispatch in
`forkContinuation`:

- empty, or equal to `inst.Tool` → `forkSessionCmdWithOptions` (today's path, unchanged)
- differs → `createSessionInGroupWithWorktreeAndOptions` (`internal/ui/home.go:11030`)
  with `command = <target tool>`, seeded by the resolver's prompt built for that tool

No new spawn machinery is required: that creator is a plain non-interactive closure
already callable from a background goroutine, in the same pattern `forkContinuation`
uses for `forkSessionCmdWithOptions`. It covers tool, group, path, worktree, and parent.

Three required guards:

1. **Exact-match tool mapping.** `createSessionTool` (`home.go:11299`) matches the
   command *exactly*, not by substring. A command string carrying flags silently yields
   `Tool = "shell"` — a data-loss-shaped failure for a handoff. `HandoffTargetTool` must
   be validated at config load against `session.PickerToolNames()` and rejected loudly,
   never silently degraded.
2. **`cursor` special case.** Its command is `"cursor agent"`, not `"cursor"`.
3. **Manual registration.** The creator returns `sessionCreatedMsg`; calling it off-loop
   means the `home.go:5362` handler never runs. Repeat the registration steps
   `forkContinuation` already performs (`context_budget_ui.go:245-254`): append to
   `h.instances`, set `h.instanceByID`, `h.forceSaveInstances()`,
   `storageWatcher.TriggerReload()`. Pass `tempID = ""` so no placeholder is orphaned.

### 4. Handoff chain-depth cap

Auto-forking on failsafe (section 2) removes the implicit brake a hard stop provided: a
session that is runaway because of a loop can spawn a successor that hits the ceiling and
forks again, indefinitely. The cap bounds that worst case.

- Track `HandoffGeneration int` as a persisted field on `session.Instance` (not in
  transient handoff state): it must survive a TUI restart and be readable at fork time.
  A human-started session is generation 0; a successor is created with
  `source.HandoffGeneration + 1`.
- Add `MaxHandoffChain int` to `ContextBudgetSettings`, **default 3**.
- When `source.HandoffGeneration + 1 > MaxHandoffChain`, failsafe reverts to a **hard
  stop** regardless of transcript availability. With the default, a human-started session
  may produce at most 3 successors in a chain (generations 1–3).

The generation also gives the notification something concrete to report
("generation 2 of 3").

## Components and boundaries

| Unit | Purpose | Depends on |
|---|---|---|
| `BuildContinuationHandoffPrompt` | Render a tool-framed prompt from a transcript tail. Pure w.r.t. the agent; reads disk only. | Existing transcript reader/renderer |
| `ResolveContinuationPrompt` | Choose curated vs mechanical prompt. No spawn knowledge. | The builder; filesystem |
| `forkContinuation` | Perform the spawn (fork or cross-tool create), register, archive source. | Resolver; the two spawn paths |
| `NextHandoffState` | Unchanged pure state machine. | Nothing (already pure) |

The resolver deliberately knows nothing about spawning, and the spawn path knows nothing
about how the prompt was produced beyond `Source` (used only for notification loudness).

## Error handling

- Resolver returns an error only when *both* sources are unusable; that is the sole
  remaining `failsafePause` trigger for a missing prompt.
- Invalid `HandoffTargetTool` fails loudly at config load, never at fork time.
- Cross-tool spawn failure falls back to `failsafePause` (no silent data loss), matching
  today's handling of a failed fork.
- Chain cap reached → hard stop with the loud notification.

## Testing

- `ResolveContinuationPrompt`: PROMPT.md present → `Source == "agent"`; absent →
  `Source == "transcript"`; both unusable → error.
- `BuildContinuationHandoffPrompt`: same-tool and cross-tool framing; the retained
  `BuildClaudeToCodexHandoffPrompt` wrapper still satisfies origin's existing tests.
- `forkContinuation`: missing PROMPT.md forks from transcript rather than pausing;
  unreadable transcript still pauses.
- Chain cap: generation at the cap hard-stops even with a readable transcript.
- Tool validation: a target tool with flags is rejected at config load, not silently
  mapped to `shell`.
- The existing `context_handoff` state-machine tests must pass **unmodified** — proof the
  machine was not disturbed.

## Upstreamability

Section 1 is the only part touching origin's code and is deliberately additive:
origin's `BuildClaudeToCodexHandoffPrompt` and its tests keep passing unchanged. It is
therefore a clean standalone PR to upstream on its own, with sections 2–4 layered
locally on top.
