# PR #2050 Round-2 Results

The branch was rebased against `origin/main` before reproduction; it was already
up to date. Reviewer comments that describe the same defect are grouped below,
but every inline, summary, and owner finding is accounted for.

## 1. `--copy` silently copied truncated data

- Reproduction: `TestAgentBoundaryModesEnumeration/copy_keeps_full_clipboard`
  failed on the reviewed head because there was no mode guard and
  `response.Content` was replaced before the clipboard branch.
- Root cause: `cmd/agent-deck/session_cmd.go:4462-4502` mutated the source
  response before selecting the output consumer.
- Fix: centralize consumer selection in `shouldBoundAgentOutput`; copy mode now
  bypasses boundary truncation and `clipboard.Copy` receives the untouched
  response.
- Evidence: targeted CLI regression suite passes in Go 1.25 container.

## 2. Remote SSH pane previews lost ANSI; JSON/quiet compatibility regressed

- Reproduction: `TestAgentBoundaryModesEnumeration` enumerates default, JSON,
  quiet, and copy consumers; the JSON and quiet cases failed on the reviewed
  head. `TestIssue1101_RemotePreview_FetchUsesPaneFlag` proves the SSH parser
  transports ANSI-rich pane content.
- Root cause: `cmd/agent-deck/session_cmd.go:4418-4447` unconditionally passed
  pane content through the ANSI-stripping budget helper before JSON
  serialization. The same unconditional branch changed existing transcript
  JSON and quiet callers.
- Fix: only default human-readable text crosses the bounded agent boundary.
  JSON (including `FetchSessionPane` over SSH), quiet, and copy modes preserve
  full raw source content and the existing JSON envelope.
- Evidence: targeted `cmd/agent-deck` and `internal/ui` tests pass; CLI help,
  conductor docs, and `skills/agent-deck` CLI reference describe the modes.

## 3. Head/tail concatenation fabricated a nonexistent line

- Reproduction: `TestPrepareAgentBoundaryOutputMarksOmittedContent` failed on
  the reviewed head because the retained prefix and suffix were adjacent.
- Root cause: `cmd/agent-deck/output_budget.go:55-64` reserved no bytes for a
  splice delimiter.
- Fix: reserve and insert an explicit `… output omitted …` marker between the
  UTF-8-safe fragments.
- Evidence: the seam regression and byte-cap/head-tail regression pass.

## 4. Tiny and overflowing token budgets lost the recovery footer

- Reproduction: `TestPrepareAgentBoundaryOutputAlwaysIncludesRecoveryFooter`
  showed `--max-tokens 1` returning only a chopped footer;
  `TestPrepareAgentBoundaryOutputSaturatesOverflow` showed `math.MaxInt`
  wrapping the byte budget negative.
- Root cause: `cmd/agent-deck/output_budget.go:40-57` multiplied unchecked and
  truncated the footer with a nonpositive content budget.
- Fix: saturate multiplication at `math.MaxInt` and treat the complete recovery
  footer as the minimum emitted budget. Tiny budgets therefore preserve the
  promised location instead of pretending the requested cap can contain it.
- Evidence: both boundary-value tests pass in the Go 1.25 container.

## 5. Writable close errors were swallowed (CodeQL #292)

- Reproduction: `TestWriteOutputReadLineReturnsCloseError` injects a writer
  whose write succeeds and close fails; the reviewed deferred `f.Close()` could
  not return that failure.
- Root cause: `cmd/agent-deck/output_budget.go:161-195` discarded the close
  result after appending the JSONL event. Snapshot cleanup also contained an
  ignored deferred close.
- Fix: `writeOutputReadLine` joins write and close errors; snapshot persistence
  tracks explicit closure and propagates cleanup close errors through its named
  return.
- Evidence: injected close-error test passes; `go vet ./...` passes.

## 6. Dot-only session IDs escaped the snapshot directory

- Reproduction: `TestOutputSnapshotPathRejectsDotOnlySessionIDs` showed `.`,
  `..`, `...`, and empty IDs resolving without error; `..` normalized above the
  session directory.
- Root cause: `cmd/agent-deck/output_budget.go:104-115` replaced separators but
  did not reject path dot segments.
- Fix: reject empty and dot-only sanitized IDs before `filepath.Join`.
- Evidence: the table-driven path regression passes.

## 7. Durability test hardcoded the non-effective data directory

- Reproduction: reviewer analysis showed the test path diverges when legacy
  markers make `GetAgentDeckDir` select the legacy directory.
- Root cause: `cmd/agent-deck/output_budget_test.go` constructed the log path
  directly from `$XDG_DATA_HOME` while production uses
  `session.GetAgentDeckDir()`.
- Fix: the test now resolves its assertion path through the same production
  helper.
- Evidence: `TestOutputSnapshotAndReadEventAreDurable` passes.

## 8. Remote coverage and touched-function documentation

- Reproduction: review correctly identified that the new helper-only tests did
  not cover the remote pane consumer and touched-function doc coverage was low.
- Root cause: coverage stopped at `prepareAgentBoundaryOutput`; several new
  helpers lacked Go doc comments.
- Fix: include the SSH remote-preview regression in the gauntlet, enumerate all
  output consumers, and document each touched helper.
- Evidence: targeted CLI/UI tests and `go vet ./...` pass.

## Verification

- Red evidence on reviewed head: new regression batch failed to compile because
  the consumer guard and close-aware writer did not exist; direct helper tests
  additionally exposed the absent seam, lost footer, overflow, and unsafe IDs.
- Green targeted gate:
  `go test ./cmd/agent-deck ./internal/ui -run 'Test(PrepareAgentBoundaryOutput|AgentBoundaryModesEnumeration|OutputSnapshot|WriteOutputReadLine|Issue1101_RemotePreview_FetchUsesPaneFlag)' -count=1`
- Required repository gate: `go build ./... && go vet ./...` passes in
  `golang:1.25` with the mounted checkout registered as a Git safe directory.
- A broader package test attempt was also made. Unrelated environment-only
  failures were observed: subprocess builds rejected the container-mounted Git
  ownership until safe-directory configuration, and tmux-dependent UI tests
  cannot run in the stock Go image. No changed-path test failed.
