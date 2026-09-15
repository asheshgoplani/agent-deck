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
