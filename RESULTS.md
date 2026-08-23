# PR #2050 final verification

## Rebase evidence

- Pre-rebase head: `4282ece17d842eeb36e29c3470acb1a20c802dfa`.
- Fetched and rebased first onto current `origin/main` at
  `7771aca6c3c356090e01f7af1f068c9290cfce46`.
- Rebased feature/fix commits: `7a1d14af` and `6c138701`.
- `git range-diff` confirmed both PR commits survived. A direct old-head/new-head
  production-path comparison showed only the expected new `main` account-command
  changes; the output-budget production and test hunks were unchanged. The only
  rebase conflict was the shared operational `RESULTS.md`.
- The rebased branch was pushed with `--force-with-lease` before review work.

## Findings addressed

All issue comments, reviews, and eight inline review comments were read. The
current head addresses every distinct finding:

1. Copy mode keeps the complete response and its complete size metadata.
2. JSON and quiet transport remain unbounded/raw; remote SSH pane JSON therefore
   retains ANSI and matches the local preview contract.
3. Truncated head and tail fragments have an explicit omission marker.
4. Tiny budgets retain the complete recovery footer and integer multiplication
   saturates instead of overflowing.
5. JSONL event writes propagate both write and close failures (CodeQL #292).
6. Empty and dot-only session IDs cannot escape the snapshot directory.
7. Durability assertions resolve the effective data directory through the same
   production helper as event recording.
8. Touched helpers are documented and the remote pane-fetch regression is in
   the targeted gate.

## Revert proofs

All Go commands ran in `golang:1.25` containers. In an isolated detached
worktree at the rebased head, the corresponding production guards were removed
while tests were left intact. The targeted test command exited 1 with behavioral
failures (not a compile failure):

- missing seam: `TestPrepareAgentBoundaryOutputMarksOmittedContent` RED;
- chopped tiny-budget footer:
  `TestPrepareAgentBoundaryOutputAlwaysIncludesRecoveryFooter` RED;
- overflowing budget: `TestPrepareAgentBoundaryOutputSaturatesOverflow` RED;
- forced bounding across all consumers: JSON, quiet, and copy cases in
  `TestAgentBoundaryModesEnumeration` RED;
- removed dot-only guard: all four cases in
  `TestOutputSnapshotPathRejectsDotOnlySessionIDs` RED;
- ignored close failure: `TestWriteOutputReadLineReturnsCloseError` RED.

The PR checkout was never mutated by this proof. With production restored, the
targeted gate passed:

```text
go test ./cmd/agent-deck ./internal/ui -run 'Test(PrepareAgentBoundaryOutput|AgentBoundaryModesEnumeration|OutputSnapshot|WriteOutputReadLine|Issue1101_RemotePreview_FetchUsesPaneFlag)' -count=1
ok github.com/asheshgoplani/agent-deck/cmd/agent-deck
ok github.com/asheshgoplani/agent-deck/internal/ui
```

The required repository gate also passed:

```text
go build ./... && go vet ./...
```

## Invariant check

- Bounds: default human text is capped; tiny budgets deliberately expand only
  enough to retain the recovery locator; overflow saturates safely.
- Ordering/fail closed: paths are validated and full snapshots are durably
  closed before bounded output is emitted; persistence failures abort emission.
- Data integrity: the source is never overwritten for copy, JSON, quiet, or SSH
  transport, and a seam marker prevents fabricated adjacency.
- Parity: the enumeration covers every output consumer (default, JSON, quiet,
  copy), while the SSH pane test covers the remote sibling path.
- Idempotence: reads do not modify session/transcript state; they append one
  audit event per invocation and atomically replace only that session/source's
  recovery snapshot.

## CI state

The final evidence commit was pushed and every required GitHub check was allowed
to finish on that exact SHA. The final external state is recorded in the PR's
check suite; no merge was performed.
