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
