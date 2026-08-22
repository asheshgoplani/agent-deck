# PR #2018 review-finding results

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
