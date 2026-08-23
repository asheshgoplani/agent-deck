# PR 2051 rebase and verification results

## Rebase evidence

- PR head before rebase: `9961cc9e0992b96b05c5665adff8090c84163cd9`.
- Previous merge base / GitHub base: `47bb210373c87aa1a90f2a319acf9174ea4b3dae`.
- Fetched `origin/main`: `7771aca6` (`fix(accounts): add launch parity and slot listing (#2053)`).
- Rebased all six PR commits onto `origin/main`; no commit was skipped.
- The only conflicts were in `RESULTS.md`, independently added on main and repeatedly updated by the PR. Main's report was retained while replaying commits and this task-specific report replaced it after source verification. No production or test file conflicted.
- A binary diff of every source/test/documentation change except `RESULTS.md` was captured before and after rebase. Both patches have SHA-256 `db9b357b4b080ab6ac6d6f7d5c15d83a2b8c42db1b0b20b40cdd56cb0162ad21`, and `cmp` returned success.
- `git range-diff 47bb2103..9961cc9e origin/main..HEAD` showed commits 3–5 patch-identical. Changes reported for commits 1, 2, and 6 were exclusively removal/replacement of their historical `RESULTS.md` text; their source hunks were preserved.
- The post-rebase diff retains all 12 non-report paths, including waiting-child completion, compact triage/blocking follow behavior, heartbeat path handling, exact generated-template migration, race-safe atomic publication, Darwin atomic exchange support, and all associated regression tests.

## Local verification

All Go commands ran in `golang:1.25` containers. No host `go test` was run.

Passed:

- CLI red-path tests: `TestChildTerminal`, `TestAllChildrenTerminal`, and `TestRunChildrenFollowEmitsWaitingAndErrorImmediately`.
- Conductor and migration red-path tests: compact triage/blocking wait, heartbeat rule path handling, generated-file migration/publication, exact prior-template migration, and preservation of edited files.
- Darwin contract: `GOOS=darwin GOARCH=amd64 go test -c ./internal/session`.
- Full repository build: `go build ./...`.
- Full repository vet: `go vet ./...`.

## CI status

The rebased branch was pushed without merging. All checks passed on rebased report head `91acb5561e934fe4bd46f0f05518f61677cfbd48`:

- Full test suite (PR gate)
- CodeQL / analyze
- golangci
- govulncheck
- Python bridge-import matrix (3.8–3.12)
- watchdog tests (3.8 and 3.12)
- PR diff-scope guard
- Release snapshot / PR drift check
- PR intake gate
- CodeRabbit review

The final report-only commit was then pushed and its exact-head checks were also allowed to run to completion before handoff.

## Round-5 base refresh

- The readiness repair began from the authoritative reviewed head `9961cc9e0992b96b05c5665adff8090c84163cd9` and retained the already-pushed round-5 verification commits through `40760de4f1a5f0e101726df2baaf9d577f67eec1`.
- GitHub `main` advanced once more to `bf50689893053c6dd33a29b21e12eb36e251d94b` (`chore(release): v1.15.0 changelog (#2059)`). The eight-commit repaired PR stack was rebased onto that exact base. This refresh had no conflicts; `git range-diff 7771aca6..40760de4 github/main..HEAD` reported all eight patches identical.
- `git merge-base --is-ancestor github/main HEAD` succeeds and `git rev-list --left-right --count github/main...HEAD` reports `0 8` before this report update. The original rebase conflict remained confined to `RESULTS.md`; no production, test, Darwin/Linux exchange, heartbeat, generated-file preservation, or child-state hunk was dropped.
- Exact-head build/test receipts and GitHub CI state for the final pushed report head are recorded in the accompanying `fix-2051-r5.RESULT.json` handoff artifact.
