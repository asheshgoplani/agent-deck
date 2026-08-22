# PR #2018 review-finding results

## CI blast-radius diagnosis

The branch was first rebased from `02a6960` onto current main `1a4e608` and
force-pushed as `4df717f`, before any workflow-specific repair. CI was allowed to
finish on that exact rebased commit.

| check | before rebase (`5a1eafb`) | after rebase (`4df717f`) | diagnosis |
|---|---|---|---|
| govulncheck | FAIL | FAIL | Package loading stopped on duplicate `truncateCell`; it was not a vulnerability finding. |
| golangci-lint | FAIL | FAIL | Typecheck stopped on the same duplicate symbol. |
| telegram-reliability | FAIL | FAIL | Targeted tests could not compile `cmd/agent-deck` because of the duplicate symbol; no Telegram assertion ran and no session leak was reported. |
| Session persistence | FAIL | FAIL | The script's binary-build step hit the duplicate symbol; the separate persistence race suite passed. |
| Go tests | cancelled after failure on the old run | FAIL | The rebased run found the duplicate symbol plus two independent `internal/ui` failures described below. |
| Eval smoke | FAIL | FAIL | Binary-building evals hit the duplicate symbol; `internal/ui` independently found the same two UI failures. |

Current main was green for govulncheck, lint, telegram reliability, and Go tests at
`1a4e608`. The pre-fix PR head had already been red for Go tests, Eval smoke, and lint,
and green for govulncheck, Telegram, and Session persistence. Rebasing removed none of
the six failures: there was no stale-base-only failure to absorb or paper over. The
common failure was instead a real integration defect: current main's `agents_cmd.go`
carried a temporary local `truncateCell` specifically intended to be replaced by the
context-inspector implementation when that branch arrived.

Fixes after remeasurement:

- Removed the temporary duplicate from `agents_cmd.go`; both agents and context output
  now use the single context-inspector helper. This restores compilation at the root,
  rather than changing five workflows independently.
- Made `ContextPager.PageUp` and `PageDown` nil-safe, satisfying the existing
  `TestContextPagerNilReceiverIsSafe` red-path test.
- Corrected `TestVerifyTabAnchorBlockIsWrappedNotClipped` to inspect the indented value
  rows it claims to govern. The old assertion stayed “inside” the block while complete
  paginated views repeated intentionally clipped header/footer chrome, producing false
  failures unrelated to the wrapped measured values.

Proof after these repairs:

- The two targeted UI regression tests pass.
- `go build ./...` and `go vet ./...` pass.
- Strict `govulncheck ./...` reaches analysis and reports zero reachable
  vulnerabilities (one required module contains an unreachable vulnerability).

## Finding 1 — `verdict --check` trusted cached gate statuses

What was wrong: `artifact.Check` inspected only the statuses serialized in
`VERDICT.json`. After a passing verdict was generated, a current G1, G2, G3, G4, or G5
pass-signal artifact could be replaced with valid JSON containing `"pass": false` and
the check would still succeed.

Reproducing test: `TestCheckRereadsEveryGatePassSignal` builds and writes a passing
verdict, then independently changes each of the five on-disk pass signals to false.
Before the fix, all five subtests failed because `Check` still reported success.

Fix: `Check` now iterates the canonical gate catalogue, calls `Tree.Inspect` for current
presence, and calls `passSignal` again for every machine-verifiable gate. It still also
rejects historical non-pass statuses recorded in the verdict. This addresses the root
cause by making the evidence tree—not cached roll-up fields—the authority at check time.

Proof: all G1–G5 mutation subtests pass in the container.

## Finding 2 — slug and gate directory could escape the repository

What was wrong: `resolveTree` accepted absolute `-gates-dir` values, parent traversal,
and multi-component slugs. `scaffold` could consequently create `G0-script.yaml`
outside the selected repository.

Reproducing tests: `TestScaffoldRejectsPathsOutsideRepository` exercises the concrete
out-of-repository scaffold write, while `TestResolveTreeRejectsUnsafePathParts` covers
absolute gate roots, `..`, slash-separated slugs, and backslash-separated slugs. Before
the fix, scaffold returned success and every unsafe resolution case was accepted.

Fix: `resolveTree` now requires a slug to be exactly one path component, requires the
gate root to be repository-relative and free of parent components, and performs a final
cleaned containment check before returning a tree. Because all verbs resolve their tree
through this function, unsafe paths are rejected before any read or write.

Proof: both path regression tests pass in the container and the scaffold test confirms
that no escaped `G0-script.yaml` is created.

## Finding 3 — README claimed evidence that was not committed

What was wrong: the context-inspector README called itself a completed six-gate example,
advertised a successful `verdict --check`, and linked to absent evidence and verdict
files.

Reproducing test: `TestContextInspectorReadmeReferencesCommittedEvidence` checks local
README links and requires every named machine-readable gate result and verdict whenever
the document makes the original completion claim. Before the fix it reported the absent
G1–G5 results and both verdict files.

Fix: the README now identifies itself as partial historical development notes, states
that no verdict can be established, links only to files that are actually committed,
and distinguishes declarations/resolutions from generated pass evidence. This fixes the
underlying false claim rather than inventing a transcript after the fact.

Proof: the documentation regression test passes in the container.

## Container verification

- `go test ./internal/sixgate/... ./tools/sixgate` — PASS. These scoped packages do not
  spawn tmux.
- `go build ./... && go vet ./...` — PASS using `golang:1.25`.
- Repository-wide `go test ./...` was intentionally not run because the repository has
  tmux-spawning suites, which the task explicitly says to skip.
- `go run ./tools/sixgate selfcheck` was also run. It has two pre-existing failures at
  this PR head: the blank-detector negative corpus triggers its own blank/orphan-percent
  rules, and the tmux identity scan flags the existing marker in `tools/sixgate/main.go`.
  Neither failure is introduced or modified by these fixes.

No tests were run on the host.
