# `session send` to Codex: the submitting Enter, and the marker that was blamed for it

Two defects on the same path, both measured on **codex-cli 0.159.3** under
**tmux 3.4** (Linux, 2026-11-12). Each has its own commit; both keep the existing
duplicate-send protection.

- Fix 1 — the message reached the pane but never submitted: the Enter was turned
  into a newline inside Codex's paste burst. Code: `internal/tmux`, `internal/session`.
- Fix 2 — a healthy submission marker was reported as corrupt while a long turn
  pushed its own turn-start record out of the backward scan window. Code:
  `internal/session`, `cmd/agent-deck`.

## Fix 1 — the body was typed unframed, so Codex made the Enter a newline

### Symptom

`agent-deck session send` to a Codex session reported success-shaped uncertainty,
and the message stayed in the composer:

```
state: typed    delivery: typed_not_submitted
reason: message typed but not submitted after 10 verification checks (issue #1413):
        the composer still holds the message despite bounded Enter retries.
        The recipient agent's input handler is not accepting Enter
```

Two real coordination messages on this host recorded exactly that (`attempts 6`
and `attempts 29`); the second sat in the composer for 31 minutes and was only
submitted by a later Enter.

### Root cause

`sendKeysAndEnterCheckedToTarget` typed a short single-line body with one
unframed `tmux send-keys -l` write and pressed Enter
`preEnterDelayDefault = 100ms` later.

Codex does not insert an unframed burst in one go: it processes the stream
character by character. While it is still ingesting, its paste-burst detector
decides an arriving Enter belongs to the paste
(`codex-rs/tui/src/bottom_pane/chat_composer/reconnect.rs`, `handle_paste_enter`):

```rust
if newline_should_insert_instead_of_submit(now) {
    self.draft.textarea.insert_str("\n");
    self.draft.paste_burst.extend_window(now);   // PASTE_ENTER_SUPPRESS_WINDOW = 120ms
    return true;
}
```

So the Enter is inserted as `\n` and the suppression window is re-armed from
that Enter. The Codex send path (`verifyContentArrival`) has no automatic Enter
retry by default, so the body simply stays in the composer.

The window is cleared the moment the body arrives as **one paste event**
(`apply_paste()` ends with `clear_after_explicit_paste()`), which is why framing
fixes it.

### Measurement (513-byte single-line body, the size that failed in production)

| transport | Enter delay | result |
|---|---|---|
| `send-keys -l` (unframed) | 50 ms | submitted |
| `send-keys -l` (unframed) | 100 ms | **not submitted** |
| `send-keys -l` (unframed) | 200 ms | **not submitted** |
| `send-keys -l` (unframed) | 500 ms | submitted |
| `paste-buffer -p -r` (framed) | 50 ms | submitted |
| `paste-buffer -p -r` (framed) | 100 ms | submitted |

A 1200-byte unframed body was left as `› [Pasted Content 1164 chars]`, unsent;
the same body framed submitted. Delay alone is not a fix — the required delay
grows with the body, so the transport has to change.

### Fix

- `internal/tmux/tmux.go`: new `Session.SetFramePastes(bool)` /
  `framesPastesForSend()`.
- `internal/tmux/tmux_sendkeys.go`: the single-line fast path in
  `sendKeysChunkedToTargetChecked` is skipped when framing is enabled, so the body
  takes the existing `load-buffer` + `paste-buffer -p -r` transport (falling back
  to the old path only when the paste cannot be staged).
- `internal/session/instance.go`: `framePasteForTool(tool)` = `IsCodexCompatible(tool)`.
- `internal/session/instance_wrapper.go`: wired in `loadCustomPatternsFromConfig`,
  beside the existing Devin `SetPreEnterDelay`, so it applies to sessions loaded
  from storage, not just newly created ones.

Every other tool keeps its byte-for-byte transport.

### Tests

`internal/tmux/frame_paste_test.go`

- a framed session must deliver a short single-line body with
  `load-buffer` + `paste-buffer -p`, never `send-keys -l`, and still submit with
  one `Enter`;
- a default session must keep typing with `send-keys -l`
  (`internal/session/frame_paste_tool_test.go` pins the tool mapping:
  `codex` true, `claude`/`pi`/`gemini`/`opencode`/`devin`/`shell`/empty false).

## Fix 2 — "invalid Codex submission marker turn generation" on a valid marker

### Symptom

After Fix 1 was live, a send to a long-running Codex coordinator still refused:

```
cannot reconcile Codex submission marker: invalid Codex submission marker turn generation
```

All 17 marker files under `locks/codex-submissions/` validated: `version: 1`,
16-byte attempt id, `prior_turn_generation` prefixed by its own
`codex_session_id`. Nothing was written by another schema.

### Root cause

The failure was the **read** of the current generation, not the marker.

1. That session's rollout was 306,962,451 bytes (293 MiB) with one turn running
   since 11:45:57Z, emitting output continuously.
2. Its own `turn_started` record was **9,437,184 bytes from EOF**.
3. `scanCodexRolloutBackward` widens only up to
   `codexTurnGenerationScanMaxBytes = 8 MiB`, so
   `latestCodexTurnIDFromRollout` returned `""` **with a nil error**.
4. `ReconcileCodexSubmissionMarker` compared that `""` with the marker's
   recorded generation and then validated it with `allowEmpty=false`, so an
   intact marker was reported as an invalid turn generation.

Read-only proof against the live rollout:

| lookup budget | newest turn start |
|---|---|
| default 8 MiB | `""` (nil error) |
| 64 MiB | `01a1cf94-3056-7232-a2c6-b46de429aa78` |

and `01a1cf94-…` is exactly the turn the persisted marker recorded. The error is
therefore transient: it appears only while the newest turn start is beyond 8 MiB
of tail, and clears as soon as a newer turn starts near EOF.

### Fix

- `internal/session/instance_transcript.go`:
  `LatestCodexTurnGenerationWithBudget(maxScanBytes)` and
  `latestCodexTurnIDFromRolloutWithin(path, maxScan)`, with
  `scanCodexRolloutBackward` refactored onto a bounded variant. The default
  (`codexTurnGenerationScanMaxBytes`, 8 MiB) is unchanged for every existing
  caller.
- `cmd/agent-deck/session_send_codex_acceptance.go`:
  `codexAcceptanceGenerationScanBytes = 64 MiB` and one helper
  `codexAcceptanceGeneration(inst)` used by the fence capture, the fence
  re-validation and the turn-advanced probe, so those three cannot disagree about
  which turn is newest.
- `internal/session/codex_submission_marker.go`: new
  `ErrCodexGenerationUndeterminable`. When a marker recorded a generation and the
  rollout can no longer produce one, the refusal names the submission, says the
  marker and its generation are intact and that nothing was typed. A marker
  created for a thread with no turn yet keeps its existing
  "unresolved submission … manually remove" message.

Duplicate protection is unchanged: an unresolved marker still refuses a second
send, and this path removes nothing and types nothing.

### Tests

`internal/session/codex_generation_scan_test.go` — a 10 MiB fixture whose
turn-start record is near the top: the default ceiling must still report "no
turn" (so the general behaviour is not silently widened) and the 64 MiB budget
must resolve `turn-long`.

`internal/session/codex_marker_generation_test.go`

- an undeterminable current generation returns `ErrCodexGenerationUndeterminable`,
  never "invalid Codex submission marker", names the attempt id, and leaves the
  marker on disk; the same marker reconciles normally once the generation is
  readable;
- a fresh-thread marker (no recorded generation) keeps its own "manually remove"
  refusal.

## Verification used here

```
go test ./internal/tmux/                                  # ok
go test ./internal/send/                                  # ok
go test ./internal/session/ -run 'Codex|Marker|Turn|Transcript|Generation'   # ok, 104s
go test ./cmd/agent-deck/  -run 'Codex|Send'                                 # ok, 134s
```

The full `internal/session` package has environment-dependent failures on this
host (`TestCrashedSession_TUIOnItsOwnServerShowsError` also fails on a pristine
`v1.16.38` worktree), unrelated to these changes.

## What this does not fix

- A composer that still holds a foreign draft blocks `session send` by design
  (`GuardComposerDraft` never clears operator input). No automated path will
  clear it and no agent should inject keys into another session's pane.
- An unresolved marker is still not cleared automatically; it is reconciled only
  by evidence from the target's own rollout, or by the operator step its refusal
  text describes.
- The opt-in `AGENTDECK_CODEX_SUBMIT_RETRY=1` remains opt-in. Fix 1 removes the
  cause it was compensating for; it is not enabled here.

## Reproduction recipe (manual, needs a Codex session)

With a Codex session idle at its composer:

```bash
T=$(agent-deck list --json | python3 -c 'import json,sys;print([s["tmux_session"] for s in json.load(sys.stdin) if s["id"]=="<session-id>"][0])')
BODY="Reply with the single word pong."            # use a long single-line body, e.g. 513 bytes
tmux -L agentdeck send-keys -l -t "$T" "$BODY"     # unframed burst
sleep 0.1
tmux -L agentdeck send-keys -t "$T" Enter          # -> a NEWLINE appears, no turn starts
```

The framed comparison:

```bash
printf '%s' "$BODY" | tmux -L agentdeck load-buffer -b probe -
tmux -L agentdeck paste-buffer -p -r -d -b probe -t "$T"
sleep 0.05
tmux -L agentdeck send-keys -t "$T" Enter          # -> the turn starts
```

Both probes submit real turns to the target session; use a scratch Codex session.
