# Task 15 — Verify and land uc-cli release provenance

**tier: mid**  
**Parallelism:** **SERIAL / NEVER PARALLEL**; separate repo with in-flight edits.

## Approved design extract (verbatim)

> This change belongs to the owning `uc-cli` repository.
>
> - Local and remote release builds stamp `org.opencontainers.image.revision` with a verified Git HEAD.
> - Revision-resolution failure aborts the release rather than emitting an empty or misleading label.
> - Dirty-tree state is reported separately and never represented as a clean revision.
> - The release reports the resulting immutable image digest, allowing deployed artifacts to be matched to the inspected image and its revision label.
>
> Agent Deck and `uc-cli` changes land independently on their respective `main` branches.

## Change

Work only in `/Users/doozyx/DoozyX/Uniqcast/uc-cli/.worktrees/feature-fix-release-provenance` on `feature/fix/release-provenance`. Before editing run `git log --oneline --decorate -10` and `git status --short`; preserve every uncommitted change and identify overlap. Existing commits `3862beb` and `7db686a` must not be recreated.

Inspect `internal/cli/release_provenance.go`, tests, local `release.go`, remote `release_remote.go`, `docs/remote-release.md`, and `plugins/uc/skills/release/SKILL.md`. Verify all four bullets by tests/code trace. Existing API: `labelRevision`, `labelDirty`, `gitRevision`, `resolveGitRevision`, `gitVerifiedHeadSHA`, `provenanceLabels`, `describeRevision`, `repoDigestFor`, `dockerRepoDigest`, `dockerImageID`, `remoteDigestCmd`, `remoteImageIDCmd`. Fill only demonstrated gaps, test-first, without overwriting in-flight hunks; stop and record an irreconcilable overlap.

Verify local/remote build commands carry verified labels, resolution failure aborts, dirty state is distinct, and repo digest is reported. Commit only owned gap fixes. Once clean and tested, land onto uc-cli `main` through its normal non-destructive merge/cherry-pick workflow only if main has not unexpectedly diverged; rerun on final main. Do not push or use `git add -A`.

## Acceptance criteria

- Four properties are evidenced for local and remote paths.
- In-flight edits are preserved.
- Final uc-cli `main` contains provenance independently of Agent Deck.

## Verification

```sh
git status --short
git log --oneline --decorate -10
go test ./internal/cli -run 'Release.*(Provenance|Revision|Digest|Dirty)|Git.*Revision' -count=1 -v
go test ./internal/cli -count=1
```

Expected in worktree and final main: tests `PASS`, final status clean, log contains provenance commits or reviewed landed equivalents. Record exact focused names if inspection shows different names.

## Interfaces

consumes:
- uc-cli commits `3862beb`, `7db686a`, current uncommitted edits, APIs listed above
- local `runRelease`; remote `runReleaseRemote` and `remoteReleaseScript`

produces:
- uc-cli `main` commit(s) satisfying revision/abort/dirty/digest contracts
- no Agent Deck source changes

## Record (append-only)

