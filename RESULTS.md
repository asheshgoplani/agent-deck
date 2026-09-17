# PR #2290: status-loop ownership refresh, round 2

PR: https://github.com/asheshgoplani/agent-deck/pull/2290

Implementation and fixtures tested: `fe6f08111a7edc906015a44a2d8236a41b469adf`.
Base: `7d2302fb8a41c5fcab7a441d56729b4b6547885a`.
Reviewed head: `49d69b468a63529ca69611e3d8fc5ee6a5e3dde5`.

[Successful Docker comparison run, including downloadable raw receipts](https://github.com/asheshgoplani/agent-deck/actions/runs/34992274574).
This receipt-only follow-up does not change production code or test fixtures.
The PR remains a draft, parked for September 25, 2026. No merge or deployment.

## Root cause confirmed

Every Codex instance formerly constructed new tmux wrappers for peer ownership reads, bypassing wrapper caches and creating quadratic subprocess traffic. Round 1 shared the reads but held the process-wide ownership mutex across serial tmux subprocesses. That could block authoritative hook publication while holding another instance's write lock.

JSON listing still validates live status. Storage lacks a per-row freshness timestamp, so treating a TUI heartbeat as row freshness would guess. Listing now skips native-ID discovery, preserving the existing JSON schema and status semantics.

## Review findings addressed

1. **Lock contention and publication:** `internal/session/codex_exclusion_cache.go:57` coalesces refreshes per socket, releases the global mutex before I/O at line 78, and merges concurrent publications at line 99. Per-pass waits release their mutex at line 125. `internal/session/instance.go:4033` releases the status instance lock during ownership refresh and line 4040 rejects superseded bootstrap work after reacquiring it. The delayed fake fails on the reviewed head and passes on this implementation for both same-socket and unrelated-socket hook publication and instance reads.
2. **Production polling and rotation:** `internal/session/codex_status_pass_regression_test.go:157` drives `StatusUpdatePass.UpdateStatus` across TTL expiry. `internal/ui/status_pass_sweep_test.go:62` drives the actual ten-worker background sweep, checks all 40 rendered statuses, and sees 80 environment reads instead of 1,600. The known-ID rotation fixture at `internal/session/codex_status_pass_regression_test.go:191` discovers the changed environment ID after actual positive-cache expiry, without calling `recordCodexOwnership` directly. Discovery took 30.496 seconds, within the existing 30-second environment TTL plus two-second metadata cadence and scheduling allowance. Race and golden-frame checks pass.
3. **Reproducible evidence:** The commands, immutable revisions, Docker digest, raw red assertions, green output, exit codes, and comparable CLI timing are included below. This file replaces the inherited PR #1952 receipt.

## Reproduce

```sh
scripts/ci/status-pass-regression.sh /tmp/status-pass-receipts
```

The script creates detached baseline/reviewed worktrees, copies identical final fixtures, and invokes unchanged baseline `Instance.UpdateStatus` through a test-only pass adapter. The lock regression was introduced in round 1, so its red comparison is the reviewed head; original quadratic behavior is tested against main's pinned base. Expected-red runs must contain the exact behavioral assertion, not a build/setup failure.

All actual tests use the same `golang:1.25` image on the same Ubuntu x86-64 hosted runner, UID/GID 1000:1000, `--init --network none --cap-drop ALL`, scratch HOME, shared module/build caches, `GOFLAGS='-mod=mod -buildvcs=false'`, and `PERF_BUDGET_MULTIPLIER=2`. Only dependency preparation has network access. Writable module metadata permits Go to update copied-test import annotations. The script records the exact image digest:

```text
["golang@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80"]
```

## Base red: production status pass beyond TTL

```text
go test -p 2 ./internal/session -run TestStatusPassSweepPinsOwnershipBeyondTTL -count=1 -v -timeout=10m
=== RUN   TestStatusPassSweepPinsOwnershipBeyondTTL
    codex_status_pass_regression_test.go:185: 12 production UpdateStatus calls across TTL: scans=12 environment reads=144 elapsed=2.184611965s
    codex_status_pass_regression_test.go:187: ownership sweep scans=12 environment reads=144, want 1 and 24
--- FAIL: TestStatusPassSweepPinsOwnershipBeyondTTL (2.19s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/internal/session	2.191s
FAIL

exit_code=1
```

## Base red: actual TUI worker sweep

```text
go test -p 2 ./internal/ui -run TestBackgroundStatusPassOwnershipLinear -count=1 -v -timeout=10m
=== RUN   TestBackgroundStatusPassOwnershipLinear
    status_pass_sweep_test.go:69: production background sweep: 40 instances, 1600 environment reads, 2m8.380000105s
    status_pass_sweep_test.go:74: environment reads=1600, want 80 (one own read and one shared peer read per instance)
--- FAIL: TestBackgroundStatusPassOwnershipLinear (128.38s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/internal/ui	128.393s
FAIL

exit_code=1
```

This delayed fixture proves the sweep spans the TTL. Its keyed-read delay also affects the baseline's peer reads. The elapsed ratio is therefore synthetic TTL stress, not a representative performance speedup. The separate CLI fixture below has no injected delays.

## Base red: comparable 100-session CLI timing

```text
go test -p 2 ./cmd/agent-deck -run TestListJSON_CodexProbeCount -count=1 -v -timeout=10m
=== RUN   TestListJSON_CodexProbeCount
    list_perf_test.go:31: 100 stale Codex sessions: 6.125297137s; show-environment subprocesses=10000
    list_perf_test.go:33: show-environment subprocesses=10000, want <=500 (linear in 100 sessions)
    list_perf_test.go:36: status listing performed native Codex session discovery
--- FAIL: TestListJSON_CodexProbeCount (7.76s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/cmd/agent-deck	10.725s
FAIL

exit_code=1
```

## Reviewed-head red: authoritative rotation and readers block

```text
go test -p 2 ./internal/session -run TestStatusPassRefreshDoesNotBlockReadersOrRotation -count=1 -v -timeout=10m
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/other
    codex_status_pass_regression_test.go:126: instance status readers blocked behind peer ownership refresh
    codex_status_pass_regression_test.go:134: authoritative hook rotation blocked behind peer ownership refresh
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow
    codex_status_pass_regression_test.go:126: instance status readers blocked behind peer ownership refresh
    codex_status_pass_regression_test.go:134: authoritative hook rotation blocked behind peer ownership refresh
--- FAIL: TestStatusPassRefreshDoesNotBlockReadersOrRotation (1.25s)
    --- FAIL: TestStatusPassRefreshDoesNotBlockReadersOrRotation/other (0.63s)
    --- FAIL: TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow (0.62s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/internal/session	1.255s
FAIL

exit_code=1
```

## Fixed green: same fixtures, production sweeps, rotation and CLI

```text
go test -p 2 ./internal/session ./internal/ui ./cmd/agent-deck -run TestStatusPass|TestBackgroundStatusPass|TestCodexExclusion|TestListJSON_CodexProbeCount|TestPerf_ColdStart_List100 -count=1 -v -timeout=10m
=== RUN   TestCodexExclusionScanLinear
    codex_exclusion_cache_test.go:70: 40 concurrent instances: 41 tmux calls, 24.797812ms
--- PASS: TestCodexExclusionScanLinear (0.03s)
=== RUN   TestCodexExclusionPassPinsSnapshotAndRefreshes
--- PASS: TestCodexExclusionPassPinsSnapshotAndRefreshes (0.00s)
=== RUN   TestCodexExclusionSocketsAndDuplicateOwners
--- PASS: TestCodexExclusionSocketsAndDuplicateOwners (0.00s)
=== RUN   TestCodexExclusionSkippedWithoutDiskScan
--- PASS: TestCodexExclusionSkippedWithoutDiskScan (0.00s)
=== RUN   TestCodexExclusionNewClaimVisibleInPinnedPass
--- PASS: TestCodexExclusionNewClaimVisibleInPinnedPass (0.00s)
=== RUN   TestCodexExclusionBootstrapCannotClaimSameRollout
--- PASS: TestCodexExclusionBootstrapCannotClaimSameRollout (0.00s)
=== RUN   TestCodexExclusionFailedPeerReadIsUnknown
--- PASS: TestCodexExclusionFailedPeerReadIsUnknown (0.00s)
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot/hook
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot/process
--- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot (0.01s)
    --- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot/hook (0.00s)
    --- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot/process (0.00s)
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/other
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow
--- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation (0.06s)
    --- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation/other (0.03s)
    --- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow (0.03s)
=== RUN   TestStatusPassSweepPinsOwnershipBeyondTTL
    codex_status_pass_regression_test.go:185: 12 production UpdateStatus calls across TTL: scans=1 environment reads=24 elapsed=2.107706308s
--- PASS: TestStatusPassSweepPinsOwnershipBeyondTTL (2.11s)
=== RUN   TestStatusPassKnownEnvironmentRotation
    codex_status_pass_regression_test.go:217: cached authoritative rotation discovered after 30.495557547s
--- PASS: TestStatusPassKnownEnvironmentRotation (30.50s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/session	32.759s
=== RUN   TestStatusPassCodexRowsGolden
--- PASS: TestStatusPassCodexRowsGolden (0.00s)
=== RUN   TestBackgroundStatusPassOwnershipLinear
    status_pass_sweep_test.go:69: production background sweep: 40 instances, 80 environment reads, 3.280648466s
--- PASS: TestBackgroundStatusPassOwnershipLinear (3.28s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/ui	3.318s
=== RUN   TestListJSON_CodexProbeCount
    list_perf_test.go:31: 100 stale Codex sessions: 83.131769ms; show-environment subprocesses=0
--- PASS: TestListJSON_CodexProbeCount (1.72s)
=== RUN   TestPerf_ColdStart_List100
    list_perf_test.go:47: list --json 100 stale Codex sessions trimmed mean=82.605426ms budget=2s
--- PASS: TestPerf_ColdStart_List100 (1.07s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	5.621s

exit_code=0
```

The same no-delay CLI workload fell from 6.125 seconds and 10,000 environment subprocesses to 83.132 milliseconds and zero environment subprocesses. The fixed trimmed mean was 82.605 milliseconds. All 100 stored waiting rows were verified as running from live fixture evidence.

## Docker race summary

```text
go test -p 2 -race ./internal/session ./internal/ui -run TestStatusPass|TestBackgroundStatusPass|TestCodexExclusion -count=1 -v -timeout=10m
=== RUN   TestCodexExclusionScanLinear
    codex_exclusion_cache_test.go:70: 40 concurrent instances: 41 tmux calls, 33.958692ms
--- PASS: TestCodexExclusionScanLinear (0.03s)
=== RUN   TestCodexExclusionPassPinsSnapshotAndRefreshes
--- PASS: TestCodexExclusionPassPinsSnapshotAndRefreshes (0.01s)
=== RUN   TestCodexExclusionSocketsAndDuplicateOwners
--- PASS: TestCodexExclusionSocketsAndDuplicateOwners (0.01s)
=== RUN   TestCodexExclusionSkippedWithoutDiskScan
--- PASS: TestCodexExclusionSkippedWithoutDiskScan (0.00s)
=== RUN   TestCodexExclusionNewClaimVisibleInPinnedPass
--- PASS: TestCodexExclusionNewClaimVisibleInPinnedPass (0.00s)
=== RUN   TestCodexExclusionBootstrapCannotClaimSameRollout
--- PASS: TestCodexExclusionBootstrapCannotClaimSameRollout (0.00s)
=== RUN   TestCodexExclusionFailedPeerReadIsUnknown
--- PASS: TestCodexExclusionFailedPeerReadIsUnknown (0.00s)
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot/hook
=== RUN   TestCodexExclusionAuthoritativeBindingAfterSnapshot/process
--- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot (0.01s)
    --- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot/hook (0.00s)
    --- PASS: TestCodexExclusionAuthoritativeBindingAfterSnapshot/process (0.00s)
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/other
=== RUN   TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow
--- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation (0.06s)
    --- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation/other (0.03s)
    --- PASS: TestStatusPassRefreshDoesNotBlockReadersOrRotation/slow (0.03s)
=== RUN   TestStatusPassSweepPinsOwnershipBeyondTTL
    codex_status_pass_regression_test.go:185: 12 production UpdateStatus calls across TTL: scans=1 environment reads=24 elapsed=2.122458902s
--- PASS: TestStatusPassSweepPinsOwnershipBeyondTTL (2.12s)
=== RUN   TestStatusPassKnownEnvironmentRotation
    codex_status_pass_regression_test.go:217: cached authoritative rotation discovered after 30.563489006s
--- PASS: TestStatusPassKnownEnvironmentRotation (30.57s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/session	34.056s
=== RUN   TestStatusPassCodexRowsGolden
--- PASS: TestStatusPassCodexRowsGolden (0.00s)
=== RUN   TestBackgroundStatusPassOwnershipLinear
    status_pass_sweep_test.go:69: production background sweep: 40 instances, 80 environment reads, 3.298419838s
--- PASS: TestBackgroundStatusPassOwnershipLinear (3.30s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/ui	4.453s

exit_code=0
```

## Additional checks and limits

- Isolated host `go build ./...` and `go vet ./...` passed. Targeted vet passed again after final fixture corrections. No host tests ran.
- Independent source review found no production blocker; its fixture cleanup and comparison findings were corrected before the accepted run.
- Local Docker initially returned HTTP 500. Hosted Docker was used instead. Early hosted attempts exposed fixture errors and container file permissions; none is accepted as behavioral red evidence. Their logs remain in the external task directory. Docker later recovered without this task restarting it.
- Standard PR CI is a separate gate. At receipt creation, all completed checks passed, including session/UI race shards, lint, native SSH acceptance on Linux/macOS, performance, persistence, and web checks. The remaining CLI shards and the receipt-only follow-up head are monitored separately; consult the PR checks for current exact-head state.
- No live-profile CPU sampling or timing of the user's real deck. No native Codex process-file rotation proof on macOS; the new fixtures cover environment-cache expiry and hook publication. Existing process-probe behavior is preserved.
- Per-bootstrap exclusion-map copies remain O(peers). Cross-process changes become visible on cache refresh. Failed ownership reads remain unknown and defer disk discovery.
- No new CLI/remote schema, real HOME-data mutation, live tmux mutation, merge, deployment, or release.

# PR #2085 rebase — results

## What was done
- Cloned `asheshgoplani/agent-deck`, fetched `pull/2085/head` as `pr-2085`, branched `carry/2085`.
- Rebased `carry/2085` onto `origin/main` (4 commits carried, contributor authorship preserved).

## Conflicts resolved (kept both intents)
1. `cmd/agent-deck/main.go` — `commandRegistry` map. `main` had added `--version`/`-v`/`--help`/`-h` and `telemetry` entries; the PR added `completion` and `__complete`. Merged into one map containing all of them.
2. `skills/agent-deck/references/cli-reference.md` — both sides added a new doc section at the same insertion point (`main` added the "update - Check for and install a new release" section, the PR added "Shell Completion"). Kept both sections back to back, update first then Shell Completion, no content dropped.
3. `README.md` — auto-merged cleanly by git (no manual edit needed despite being flagged conflicting in the review).

No other files conflicted. `cmd/agent-deck/completion_cmd.go`, `completion_cmd_test.go`, and the `remote_cmd.go` changes from the PR applied without conflict.

## Verification
- `go build ./...` — clean, no output/errors.
- `go vet ./...` — clean, no output/errors.
- `golangci-lint run ./cmd/agent-deck/...` — skipped: host golangci-lint is v1, repo config requires v2 (`Error: you are using a configuration file for golangci-lint v2 with golangci-lint v1`). Not run.
- Docker (`golang:1.25`, `--network none`, serialized via `/tmp/agentdeck-docker.lock.d`): `go test ./cmd/agent-deck/... -run "Completion|Complete" -v`
  - All completion-related tests pass: `TestCompletionTree_Consistent`, `TestCompletionTopLevelNames_IncludesCoreCommands`, `TestCompletionRules_NoDuplicateKeys`, `TestCompletionRules_KnownCases`, `TestBashCompletionScript_WellFormed`, `TestZshCompletionScript_WellFormed`, `TestFishCompletionScript_WellFormed`, `TestCompletionScripts_ShellSyntaxIsValid` (bash passes; zsh/fish subtests skip — shells not installed in the base `golang:1.25` image, not a code defect), `TestCompletionScripts_CarryEverySubcommandList`, `TestHandleComplete_*` (Sessions/UnknownProfileIsSilent/Remotes/RemotesListsConfigured/RemoteSessionsUnknownRemoteIsSilent/RemoteSessionsMissingRemoteArgIsSilent/Agents/Groups), `TestPrintProfileCompletions_FiltersInternalNames`, `TestPrintCompletionHelp_WritesUsage`, `TestHandleCompletion_DispatchesEachShellAndHelp` (all subtests), `TestCompletionHelperProcess`, `TestHandleCompletion_NoArgsExitsNonZeroWithUsageOnStderr`, `TestHandleCompletion_UnknownShellExitsNonZeroWithMessage`.
  - Full package run: `ok github.com/asheshgoplani/agent-deck/cmd/agent-deck 45.752s`, no failures.

## State
- Branch `carry/2085` sits on top of current `origin/main` (through `7d2302fb chore(release): v1.16.10 (#2276)`), no longer marked CONFLICTING.
- Not pushed anywhere (per task's no-push, no-GitHub-writes rule). Local only, at `/tmp/exec-heldpr-2085/src`, branch `carry/2085`, HEAD `06f72a12`.
- No `go test` was run against the real agent-deck data directory or host tmux; all testing was Docker-only, per the repo's tmux-hygiene rules.

# PR #2120 — rebase carry, verification

## What was done

- Cloned `asheshgoplani/agent-deck` into an isolated dir (`/tmp/exec-heldpr-2120/src`).
- Fetched PR #2120 head (`fix/2061-shell-window-sizing`), checked it out, branched
  `carry/2120`, and rebased onto `origin/main` (60 commits behind).
- Rebase was clean — no conflicts. The contributor's single commit
  (`fix(tmux): retain sizing policy for Deck shell windows`) is preserved intact
  as the only commit ahead of `origin/main`.

New head: `042e8a359976fb72ba5c85f51bcb0ad0703cd06b` (branch `carry/2120`, base
`origin/main`).

Diff vs `origin/main` (unchanged from the PR's own diff, just replayed on a fresh base):

```
 internal/tmux/shell_window_size_test.go         | 183 ++++++++++++++++++++++++
 internal/tmux/tmux.go                           |  31 +++-
 skills/agent-deck/references/troubleshooting.md |  27 ++--
 3 files changed, 227 insertions(+), 14 deletions(-)
```

## Build / vet / lint

- `go build ./...` — clean, no output.
- `go vet ./...` — clean, no output.
- `golangci-lint` (host binary is v1.64.8, repo config targets v2, so it refuses
  to run — this is a pre-existing environment mismatch, not something this PR
  can fix). Ran the repo's pinned-equivalent lint via
  `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.0`:
  - `./internal/tmux/...` — **0 issues.** This confirms the review finding: the
    gosec G702 flagged at `internal/tmux/socket.go:212` on the stale branch is
    gone now that the branch carries current `main`'s `#nosec G204,G702`
    annotation on that call site. It was a stale-branch artifact, not a real
    finding introduced by this PR.
  - Full-repo `./...` — 5 unrelated pre-existing gosec findings, all in files
    this PR never touches (`internal/agents/cron.go:92,140`,
    `internal/git/git.go:260,267,980`). Confirms the PR's own package is clean.

## Tests

Per policy, no local `go test` outside the sandboxed Docker container. Ran the
touched package only, serialized via the docker lock:

```
docker run --rm --init -u 1000:1000 --network none --cap-drop ALL \
  -v "$PWD":/src -w /src -v agentdeck-gomod:/tmp/gomod \
  -e HOME=/tmp/h -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/h/.cache -e GOFLAGS=-mod=mod \
  agentdeck-gotest:1.25-tmux sh -c 'go test ./internal/tmux/...'
```

(Used the pre-built `agentdeck-gotest:1.25-tmux` image, which already has tmux
installed, since `--network none` blocks `apt-get install` inside the plain
`golang:1.25` image from the base recipe.)

- Full package run: 1 failure — `TestKill_LiveSessionThenSecondKillBothSucceed`
  (`kill_idempotent_test.go:47`, unrelated to this PR's files).
- Re-ran that single test 3x in isolation: passed every time (`0.02-0.06s`
  each). This is a pre-existing flake under concurrent full-package load, not a
  regression from the rebase or this PR's change.
- Re-ran the PR's own new tests, `TestSession_NewShellWindowSizePolicy` and its
  five subtests, 2x back-to-back: **all pass, every run, every subtest.**

## Conclusion

- Rebase: clean, 0 conflicts, contributor's commit preserved verbatim.
- `go build` / `go vet`: clean.
- Lint on the touched package: 0 issues — the reported gosec G702 stale-branch
  artifact is confirmed cleared by the rebase.
- Tests: the PR's own new tests pass reliably; the one observed failure in the
  package is a pre-existing, reproducible-only-under-load flake in an untouched
  test file, confirmed unrelated by isolated re-runs.

No code changes were made beyond the rebase itself (no conflicts to resolve).
`PR-NOTE.md` in this same directory has the trimmed evidence for the PR body.