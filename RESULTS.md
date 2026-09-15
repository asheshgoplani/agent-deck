# Runtime health, PR #2289

PR: https://github.com/asheshgoplani/agent-deck/pull/2289

Draft, stacked on #2290, parked until the 2026-09-25 release gate.

## Root cause

Confirmed #2286: the application lacked runtime observations. The implementation adds local, default-on native process sampling, bounded profile-local JSONL, CLI text/JSON reports and remote execution, doctor integration and a one-time footer warning. Missing measurements remain unknown. The 100-Codex-session performance gate also depends on #2284's shared ownership scan.

## Round 2 review fixes

1. The headless web acceptance subprocess now receives a fresh private `TMUX_TMPDIR` after environment filtering. `ShortTmuxSocket` cleanup captures that exact directory and kills its servers before removal. The Docker acceptance script starts a sentinel on the container's default socket, reproduces the reviewed binding mutation, then requires unchanged bindings and sessions on the fixed source.
2. `scripts/runtime-health/performance.sh` archives the tested head twice, reverses the dependency's status algorithm for the red variant, retains the same health instrumentation and final harness, and records source SHAs, the reversal patch, harness SHA256, multiplier, command and actual exit statuses. It rejects compilation failures or skips as performance evidence.
3. Health SSH parity now fails explicitly without the OpenSSH client. A subprocess regression exercises an empty PATH. Acceptance restores the reviewed test implementation to prove that its skip is rejected, then requires the fixed missing-client failure and actual authenticated SSH JSON equality.

## Verification

The `Runtime health acceptance` workflow runs tests in disposable Docker containers as UID 1000, with no network and all capabilities dropped. It retains source-stamped raw red/green logs and sentinel snapshots for 90 days. The final execution receipt will be recorded here after CI completes.

Local Docker Desktop reports running but the engine API times out. No host tests were substituted. Host `go build ./... && go vet ./...` passed on the implementation before this evidence commit.

The former root RESULTS.md was an unrelated PR #1952 report. It has been preserved outside this checkout as RESULTS-inherited-1952.md.

## Not covered

No production profile or live host tmux server is used. No merge or deployment occurs. Native Darwin sampler runtime behavior remains unverified under the Docker-only test requirement. Synthetic SSH tests do not prove production network latency. Synthetic performance timing remains host-load dependent; the command budget is not scaled by the timing multiplier.
