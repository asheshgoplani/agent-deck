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

# Runtime health, PR #2289

PR: https://github.com/asheshgoplani/agent-deck/pull/2289

Draft, stacked on #2290, parked until the 2026-09-25 release gate.

## Root cause

Confirmed #2286: the application lacked runtime observations. The implementation adds local, default-on native process sampling, bounded profile-local JSONL, CLI text/JSON reports and remote execution, doctor integration and a one-time footer warning. Missing measurements remain unknown. The 100-Codex-session performance gate also depends on #2284's shared ownership scan.

## Round 2 review fixes

1. The headless web acceptance subprocess now receives a fresh private `TMUX_TMPDIR` after environment filtering. `ShortTmuxSocket` cleanup captures that exact directory and kills its servers before removal. The Docker acceptance script starts a sentinel on the container's default socket, reproduces the reviewed binding mutation, then requires unchanged bindings and sessions on the fixed source.
2. `scripts/runtime-health/performance.sh` archives the tested head twice, reverses the dependency's status algorithm for the red variant, retains the same health instrumentation and final harness, and records source SHAs, the reversal patch, harness SHA256, multiplier, command and actual exit statuses. It rejects compilation failures or skips as performance evidence.
3. Health SSH parity now fails explicitly without the OpenSSH client. A subprocess regression exercises an empty PATH. Acceptance restores the reviewed test implementation to prove that its skip is rejected, then requires the fixed missing-client failure and actual authenticated SSH JSON equality.

## Verified Docker receipt

Implementation head: `086c852df40b244e72811defeb7f31d32070db12`.

[Successful Docker acceptance run](https://github.com/asheshgoplani/agent-deck/actions/runs/34989986511), [raw artifact](https://github.com/asheshgoplani/agent-deck/actions/runs/34989986511/artifacts/10405451920). Logs and source hashes are retained in the artifact for 90 days; the actual outputs below are retained in this report. This report commit changes documentation only.

The workflow builds `scripts/runtime-health/Dockerfile` from Go 1.25, installs tmux and OpenSSH, and primes the module cache before disabling networking. Both acceptance commands execute with:

```sh
docker run --rm --init -u 1000:1000 --network none --cap-drop ALL \
  -v "$PWD":/src:ro -w /src -v "$PWD/evidence":/evidence \
  -v runtime-health-gomod:/tmp/gomod \
  -e HOME=/tmp/h -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/h/.cache \
  -e GOFLAGS=-mod=mod -e PERF_BUDGET_MULTIPLIER=2 \
  runtime-health-test bash scripts/runtime-health/acceptance.sh /src /evidence
# Repeat the same docker command with this script and output directory:
# bash scripts/runtime-health/performance.sh /src /evidence/perf
```

### Finding 1: default-server safety

Source: `cmd/agent-deck/health_integration_test.go:164-175`. The private directory is assigned after filtering; cleanup uses `internal/testutil/tmuxenv.go`'s existing absolute-socket kill-before-remove implementation.

`acceptance.sh` archives the implementation head twice and restores only `health_integration_test.go` from reviewed head `6574d29ef262f12648fc0233f9bc962779abfe87` in the red tree. It runs the real web startup test against a container-default sentinel server. The baseline test itself exits zero but mutates the binding, which makes the sentinel assertion exit 1. The fixed tests leave every binding and session unchanged, with both comparisons exiting zero.

The actual changed binding was:

```diff
-bind-key -T root MouseDown1StatusRight display-message runtime-health-sentinel
+bind-key -T root MouseDown1StatusRight if-shell -F "#{m:agentdeck_*,#{session_name}}" detach-client ''
```

Spacing above is normalized; full unmodified binding snapshots are in the artifact. Both fixed-run session snapshots were `$0:health-review-sentinel:1`.

```text
=== RUN   TestRuntimeHealthHeadlessWebStartup
--- PASS: TestRuntimeHealthHeadlessWebStartup (0.11s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	1.388s
/evidence/before.bindings /evidence/after-red.bindings differ: char 22437, line 249
ssh_red_exit=1
web_baseline_test_exit=0
sentinel_red_exit=1
acceptance_green_exit=0
sentinel_bindings_exit=0
sentinel_sessions_exit=0
```

### Finding 2: identical-harness performance failure and pass

Original-main baseline: `7d2302fb8a41c5fcab7a441d56729b4b6547885a`.
Dependency: `49d69b468a63529ca69611e3d8fc5ee6a5e3dde5`.
Green source: `086c852df40b244e72811defeb7f31d32070db12`.

The red source is a green-head archive with `git diff BASELINE DEPENDENCY -- internal/session/instance.go internal/ui/home.go cmd/agent-deck/main.go internal/web/session_data_service.go` applied in reverse and the added `internal/session/codex_exclusion_cache.go` moved outside the build tree. This restores the original-main status algorithm while preserving the same health production code, measurement instrumentation and final harness. Unused dependency tmux helpers remain. It is an explicit overlay, not a claim that unchanged main contains health APIs.

Final harness SHA256, identical in red and green: `2065b3179328d613099a0e36a5647e8211bfaa9950c35f0bd04a6f5fecd832ba`.

Reversal patch SHA256: `d9da4ad4d84137124a2aeda5de1611235c0022403d49676f89ca123b443ff4dd`.

Go: `go1.25.14 linux/amd64`. Multiplier: `2`, giving a 500 ms time budget. The 200-command and fewer-than-512-descriptor budgets stay unscaled.

Identical command: `go test -tags runtimehealthperf ./internal/ui -run '^TestPerf_RuntimeHealthCodex$' -count=1 -v -timeout 120s`.

Red exit: `1`. Green exit: `0`. Acceptance requires measured output plus the command-budget failure; compile failures and skips cannot qualify.

```text
=== RUN   TestPerf_RuntimeHealthCodex
    runtime_health_perf_test.go:124: tmux commands: map[capture-pane:100 display-message:19 has-session:38 list-panes:1 list-sessions:101 list-windows:1 set-environment:1 set-option:5 show-environment:9901 show-options:2]
    runtime_health_perf_test.go:154: 100 fake sessions: codex=true pass=2569.90 ms, fds=7, tmux starts=10169, multiplier=2
    runtime_health_perf_test.go:156: pass 2569.90 ms exceeds budget
    runtime_health_perf_test.go:162: tmux calls=10169 outside budget
--- FAIL: TestPerf_RuntimeHealthCodex (2.78s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/internal/ui	2.790s
FAIL
=== RUN   TestPerf_RuntimeHealthCodex
    runtime_health_perf_test.go:124: tmux commands: map[capture-pane:100 list-panes:1 list-sessions:1 list-windows:1 set-environment:1 set-option:5 show-environment:1 show-options:2]
    runtime_health_perf_test.go:154: 100 fake sessions: codex=true pass=38.22 ms, fds=7, tmux starts=112, multiplier=2
--- PASS: TestPerf_RuntimeHealthCodex (0.24s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/ui	0.257s
```

### Finding 3: missing SSH fails; real SSH executes

Source: `cmd/agent-deck/health_integration_test.go:77-80`, `cmd/agent-deck/health_acceptance_test.go:12-28`.

The final negative harness is identical on reviewed and fixed sources. It calls the acceptance test with an empty PATH in a subprocess. Reviewed code skips and returns zero; the regression rejects this with exit 1. Fixed code fails explicitly without SSH, then the normal acceptance executes authenticated OpenSSH and compares local and remote JSON from distinct profile fixtures.

Red output:

```text
=== RUN   TestHealthRemoteExecRequiresSSH
    health_acceptance_test.go:26: missing SSH must fail acceptance explicitly; error=<nil>
        === RUN   TestHealthRemoteExecRequiresSSH
            health_integration_test.go:87: OpenSSH client required
        --- SKIP: TestHealthRemoteExecRequiresSSH (0.00s)
        PASS
--- FAIL: TestHealthRemoteExecRequiresSSH (0.01s)
FAIL
FAIL	github.com/asheshgoplani/agent-deck/cmd/agent-deck	2.786s
FAIL
```

Fixed missing-dependency failure and full executed CLI acceptance, outer exit `0`:

```text
=== RUN   TestHealthRemoteExecRequiresSSH
    health_acceptance_test.go:28: missing-dependency acceptance failed as required:
        === RUN   TestHealthRemoteExecRequiresSSH
            health_integration_test.go:79: health parity requires OpenSSH client: exec: "ssh": executable file not found in $PATH
        --- FAIL: TestHealthRemoteExecRequiresSSH (0.00s)
        FAIL
--- PASS: TestHealthRemoteExecRequiresSSH (0.01s)
=== RUN   TestHealthCLIEmptyAndValidation
--- PASS: TestHealthCLIEmptyAndValidation (0.04s)
=== RUN   TestRuntimeHealthStartupConfigAndProfileIsolation
=== RUN   TestRuntimeHealthStartupConfigAndProfileIsolation/default
=== RUN   TestRuntimeHealthStartupConfigAndProfileIsolation/enabled
=== RUN   TestRuntimeHealthStartupConfigAndProfileIsolation/disabled
--- PASS: TestRuntimeHealthStartupConfigAndProfileIsolation (0.00s)
    --- PASS: TestRuntimeHealthStartupConfigAndProfileIsolation/default (0.00s)
    --- PASS: TestRuntimeHealthStartupConfigAndProfileIsolation/enabled (0.00s)
    --- PASS: TestRuntimeHealthStartupConfigAndProfileIsolation/disabled (0.00s)
=== RUN   TestHealthRemoteExecJSONParity
--- PASS: TestHealthRemoteExecJSONParity (0.11s)
=== RUN   TestHealthReadDoesNotStartSampler
--- PASS: TestHealthReadDoesNotStartSampler (0.01s)
=== RUN   TestRuntimeHealthHeadlessWebStartup
--- PASS: TestRuntimeHealthHeadlessWebStartup (0.37s)
=== RUN   TestDoctorUnreadableHealthPreservesAccounts
--- PASS: TestDoctorUnreadableHealthPreservesAccounts (0.01s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	3.374s
```

Findings 1 and 3 use the reviewed implementation as the behavioral baseline because original main has no runtime-health acceptance code. Finding 2 restores the original-main status algorithm explicitly. No compile failure is accepted as a behavioral red result.

## Other verification and evidence limits

Host `go build ./... && go vet ./...` passed on the implementation. Local Docker Desktop reports running but its engine API timed out, so no host tests were substituted; all new executed acceptance tests ran in CI Docker. Existing CI also runs the normal test shards, runtime-health budgets, performance/persistence regressions, and native SSH acceptance. Consult the final PR checks for their head-specific status.

The unrelated former root RESULTS.md for PR #1952 was preserved outside this checkout as RESULTS-inherited-1952.md.

## Not covered

No production profile or live host tmux server is used. No merge or deployment occurs. Native Darwin sampler runtime behavior remains unverified under the Docker-only test requirement. Synthetic SSH tests do not prove production network latency. Synthetic performance timing remains host-load dependent; the command budget is not scaled by the timing multiplier.
