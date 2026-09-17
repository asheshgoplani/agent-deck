## Summary

Round 3 clears the two review findings from the round-2 review, so this
carry is fully green and internally consistent:

- Fixed two tests that were previously disclosed-red rather than actually
  fixed:
  - `TestValidateRejectsAvailableItemsThatClaimACost`: reworded the
    validator's rejection message so it names both the breach (a positive
    cost) and the allowed values (a certain zero or an explicit unknown),
    matching what both tests that pin this contract expect. The rejection
    logic itself was already correct.
  - `TestInspectReportsALowMatchRateRatherThanGuessing`: the Codex adapter
    already computed a per-file "this file no longer matches the injected
    block" caveat, but only attached it to the item, never to the report.
    A separate, newly-added generic "read from disk as of now" caveat was
    the only one visible at the report level, so a drifted file's own name
    never showed up where the report's caveats are listed. Now both the
    generic and the per-file caveat are recorded on the report.
- Completed the self-check → reconciliation rename: the pager's
  always-visible footer still said `self-check:` while the Verify tab and
  the CLI overview already said `reconciliation:`. Renamed the footer
  label, updated the glossary to match, and added a test that pins the
  footer's text so this can't silently drift again.

No behavior change beyond wording/labels and where an existing caveat gets
recorded; no fixtures needed changes.

## Test plan

- [x] `go build ./...` — clean
- [x] `go vet ./...` — clean
- [x] `go test ./internal/ctxinspect/... ./internal/ui/... ./cmd/agent-deck/...`
      in the sandboxed test container — all packages pass except the
      known sandbox-only uid-1000/no-SSH-user tests (`TestRemote*Parity`,
      `TestHealthRemoteExecJSONParity`), which fail for the same reason on
      main and are unrelated to this change.
- [x] Read-only real-session check: `agent-deck session context <id>`
      output is unchanged between this branch and the prior commit — the
      footer label only renders in the interactive TUI, not the CLI
      command used for this check.

---

Issue #2079 names two call sites where a truncated `launch --message-file`
paste can read as a clean delivery: `sendMessageWhenReady`
(`internal/session/instance.go`) and the `launch --no-wait` post-send
verifier, `pollPromptConsumed` (`cmd/agent-deck/launch_verify_prompt.go`).
Round 1 of this branch fixed only the first. This round covers the second.

- Claude's composer collapses a framed multi-line paste behind a
  `[Pasted text #N +M lines]` marker whether the paste arrived whole or was
  cut short. `pollPromptConsumed` now checks the declared `M` against the
  message's real line count once the composer looks consumed, the same
  primitives (`send.ExpectedPasteMarkerLines`, `send.PasteMarkerLineCounts`)
  round 1 added for the first call site.
- A marker declaring fewer lines than the message is reported as
  `"prompt truncated in transit"` instead of success, and the one retry is
  skipped (retyping onto an already-submitted fragment risks a duplicate
  submission).
- A consumed-looking composer with no marker at all, for a message that
  expects one, is reported as unknown — never as success.
- A marker that declares at least as many lines as the message (or a
  single-line message, which never collapses behind a marker) is unchanged:
  a silent success.

## Test plan

- [x] New failing-first test file
      `cmd/agent-deck/issue2079_launch_verify_prompt_test.go`, using the
      existing `mockSendRetryTarget` fake.
- [x] Revert-proof in `golang:1.25`: production check disabled → both new
      tests fail with the expected messages; check restored → all pass
      alongside the full pre-existing `TestVerifyPromptConsumedAfterLaunch_*`
      suite.
- [x] `go build ./...` and `go vet ./...`: PASS (host).
- [x] `go test ./cmd/agent-deck/...` and `./internal/send/...`: PASS
      (`golang:1.25`).

See `RESULTS.md` for full details and caveats.
