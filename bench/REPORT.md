# Performance report

## Findings and evidence status

The suite exposes the original fleet-scaling problem: at 100 sessions, original main spent **23.600 s p50** in `list --json` and made **4,020 tmux calls p50**. The health stack, which includes the status-loop fix, measured **486 ms and 102 calls** on the corrected mixed-tool fixture. At 500 sessions the original list invocation exceeded its 120-second timeout; the health stack completed at **7.555 s p50 / 11.257 s p95**. Large-fleet CLI and status work therefore remain material costs after the fix.

These measurements are exploratory baselines for the 2026-09-25 release program, not release approval. Source changes, CI, merge and deployment are separate gates.

| Evidence | Production revision | Coverage | Completion status |
|---|---|---|---|
| [Accepted health-stack baseline](baseline/linux-arm64.json), [table](baseline/linux-arm64.md) | `6574d29ef262f12648fc0233f9bc962779abfe87` | 10/100/500 sessions, N=3, 72 metric rows including resources | Complete JSON and full Markdown emitted; Docker wrapper subsequently exited 125 with a disk I/O error |
| [Original main, PARTIAL](baseline/original-main-linux-arm64.partial.json), [table](baseline/original-main-linux-arm64.partial.md) | `7d2302fb8a41c5fcab7a441d56729b4b6547885a` | 10/100 sessions, N=3, 44 metric rows; resource sampler unavailable | Recovered completed workers; 500-session list timed out after 120 s |
| Earlier shell-only diagnostic (retained locally) | Original main | Fixture lost its intended tool mix | Excluded from accepted baselines and status-fix claims |

The health run's final wrapper error was `Error waiting for container`, followed by an input/output error writing Docker's `io.containerd.metadata.v1.bolt/meta.db`. The measurements were already serialized and the full table printed. We retain the complete data as accepted measurement evidence, but do not describe the wrapper invocation as a clean exit or infer successful Docker teardown from that invocation. The original 500-session timeout is a censored failure, not a 120-second percentile or an estimated value.

Both accepted datasets use Linux arm64 Docker on the shared Apple Silicon host, machine class `docker-arm64-16cpu-shared`, with 16 virtual CPUs and approximately 15.6 GiB Docker memory, seed 2286 and three repeated samples per fleet size. The revision identifies production code; the driver and test harness are benchmark overlays. Compilation and fleet creation are outside the timings. Shared host contention makes wall-clock ratios approximate. Native subprocess counts are stronger evidence of changed work, though overlapping instrumented activity and cache expiry can vary them.

## Accepted baseline

Each cell is p50 / p95. Time units are milliseconds unless stated otherwise. Three-run nearest-rank p95 is the maximum observation, not a well-estimated production tail. Key-repeat rows aggregate 300 individual frames per size.

| Metric | 10 sessions | 100 sessions | 500 sessions |
|---|---:|---:|---:|
| List JSON | 43.937 / 72.794 | 486.200 / 570.546 | 7554.833 / 11257.221 |
| Status pass | 50.657 / 60.193 | 363.218 / 1001.814 | 2012.982 / 3435.681 |
| Status tmux calls, count | 30 / 32 | 183 / 185 | 849 / 1120 |
| List tmux calls, count | 12 / 12 | 102 / 102 | 1240 / 1318 |
| Cold populated PTY frame | 5103.959 / 5238.038 | 5095.239 / 5167.258 | 5156.216 / 5305.418 |
| Shell send + executed ACK | 6681.810 / 6745.562 | 6825.685 / 7987.728 | 7100.589 / 7819.029 |
| Session show | 16.805 / 27.928 | 25.288 / 28.245 | 53.854 / 129.625 |
| Fast remote refresh | 0.804 / 1.020 | 1.748 / 57.625 | 0.893 / 3.052 |
| Injected slow remote refresh | 6004.549 / 6005.524 | 6003.055 / 6191.798 | 6004.913 / 6005.080 |
| Auth-failed remote refresh | 2.340 / 7.983 | 1.525 / 9.943 | 3.032 / 3.446 |
| First populated model frame | 13.891 / 25.626 | 81.367 / 121.929 | 191.452 / 227.724 |
| Key-repeat Update/View | 0.560 / 0.807 | 0.840 / 2.480 | 0.839 / 1.844 |
| Harness RSS, MiB | 41.758 / 44.898 | 42.973 / 45.484 | 50.441 / 51.012 |
| Harness open fds, count | 10 / 10 | 10 / 10 | 10 / 10 |
| Harness goroutines, count | 4 / 4 | 4 / 4 | 4 / 4 |
| Old orphan hooks remaining, count | 30 / 30 | 300 / 300 | 1500 / 1500 |

## Five largest measured costs

Ranked by observed wall time across these fixtures. This is a workload ranking, not proof that all five are implementation defects; the intentionally slow remote is a fault scenario.

| Rank | Cost and evidence | Why it costs time | Next action and expected gain | Risk and evidence needed |
|---:|---|---|---|---|
| 1 | Large-fleet list: original 100-session p50 23.600 s; original 500 timed out; health 500 p50 7.555 s | Original counts establish excessive native tmux work. Health reduces that work substantially, but 500-session p50 still makes 1,240 calls. The remaining cause is not isolated by this run. | Verify #2290, then profile the residual 500-session path and cache-expiry behavior. Expected gain is fewer subprocesses per list; no residual latency target claimed yet. | Caching must preserve liveness, identity and unknown/error states. Repeat with a quieter host before attributing remaining nonlinear wall time. |
| 2 | Shell send: health p50 6.682 to 7.101 s; 98 calls at every size | Product send waits for agent submission confirmation that a shell cannot provide. The shell executes a complete ACK line, but `send_cli_unconfirmed=1` in every sample. | Investigate the confirmation path using an actual agent-composer fixture before changing behavior. Avoidable confirmation waiting is a candidate, not a proven safe optimization. | Removing checks could turn an unconfirmed submission into false success. These numbers do not measure agent acceptance or model response. |
| 3 | Slow remote: approximately 6.00 s by design | Fake SSH deliberately sleeps six seconds before returning valid JSON. Direct request timing necessarily includes that delay. | Verify #2285 cancellation/backoff behavior and keep slow or failed hosts out of unrelated UI work. Expected gain is responsiveness while requests remain slow, not reducing the injected sleep. | One-shot refresh latency cannot prove retry cadence, startup coupling, orderly exit cancellation or real SSH behavior. |
| 4 | Cold populated terminal frame: health p50 5.095 to 5.156 s | Measured from process spawn until a seeded session title reaches the PTY, including startup workers and configured remotes. Its nearly fixed delay is not explained by the much shorter model-only frame timing. | Trace startup waits and remote scheduling under #2285. Expected gain is earlier visible sessions if startup is waiting on unrelated work; attribution is still open. | Do not substitute a model View call or a splash frame for populated terminal output. The remote branch single-run diagnostic does not establish a startup speedup. |
| 5 | Status sweep: health 500 p50 2.013 s / p95 3.436 s, 849 calls p50 | A real synchronous production sweep polls the mixed fleet. The fixed scan is much cheaper at 100 sessions, but substantial per-session work remains at 500. | Preserve the status fix, then measure cached and active-session paths separately. Expected gain is reduced repeated work without stale status; no additional production change is claimed here. | Health's 250 ms status budget is still exceeded at 100 and 500 sessions. The harness disables eager background workers, so this is not a measurement of a fully running event-driven TUI. |

## Status-fix comparison

At 100 sessions, the corrected original-main and health-stack observations are:

| Metric | Original main p50 / p95 | Health stack p50 / p95 | p50 change |
|---|---:|---:|---:|
| List, ms | 23,600.134 / 27,110.836 | 486.200 / 570.546 | 97.9% lower |
| List tmux calls | 4,020 / 4,096 | 102 / 102 | 97.5% fewer |
| Status pass, ms | 1,814.003 / 3,101.156 | 363.218 / 1,001.814 | 80.0% lower |
| Status tmux calls | 3,485 / 3,526 | 183 / 185 | 94.7% fewer |

This is a comparison of production revisions including both runtime-health and status-loop changes, not an isolated causal estimate of health instrumentation overhead. The corrected fixture verifies the observed Claude/Codex/shell mix after listing. The count reductions support the status-scan improvement; shared-machine timing and only three runs limit precision. Missing original 500-session and resource readings remain unknown. The strict full-baseline comparator is not appropriate for silently accepting that missing coverage.

## Hooks and remote worker verification

The accepted health baseline leaves 30/300/1,500 seeded old orphan hook files at sizes 10/100/500. Its stable 10 open fds do not disprove the reported leak: those fds belong to the test harness with background workers disabled, and Linux inotify does not reproduce Darwin kqueue's descriptor-per-file behavior.

The [hooks worker diagnostic](verification/hooks-linux-arm64.json), revision `4f6b51214637f5aaff06aaf3f123f76de48196c4`, removed all 30 seeded orphan files at 10 sessions. This single successful run demonstrates fixture sensitivity to pruning, not long-lived memory or Darwin fd recovery. The [remote worker diagnostic](verification/remote-linux-arm64.json), revision `87cf89e544f4366ec464d4420160676793fa8bc9`, completed the same 10-session suite. Both diagnostic benchmark commands exited 0; N=1 provides no meaningful tail estimate. Their cold populated PTY frames were about 5.789 s, so no remote-startup speedup is established.

The remote worker's focused polling, transport and golden-frame tests printed PASS after rerunning from the correct package directory. That corrected test run then encountered the same Docker metadata I/O completion error and exited 125. Recorded test PASS output and a clean test-command exit are separate claims. The earlier wrong-directory golden-file lookup failure is not a passing result.

## Separate group-activity improvement

The candidate changes only per-group activity aggregation used by active-on-top and populated-on-top list modes. It scans eligible sessions to combine flags, then propagates them to ancestors once per group. It leaves the three already-owned production areas untouched. Archive filtering, empty groups, ancestor flags and ordering behavior must remain identical.

Warm Docker microbenchmarks used five samples and `GOMAXPROCS=2`. These results are separate from the three-run fleet baseline and do not establish an end-to-end frame speedup.

| Sessions | Before median, ns/op | After median, ns/op | Before / after bytes | Before / after allocations |
|---:|---:|---:|---:|---:|
| 10 | 3,691 | 4,182 | 1,592 / 1,592 | 35 / 35 |
| 100 | 52,068 | 15,069 | 12,264 / 6,104 | 309 / 99 |
| 500 | 218,711 | 22,769 | 47,464 / 6,104 | 1,509 / 99 |

At 500 sessions the observed median fell 89.6% and allocations fell 93.4%. At 10 sessions time increased 13.3% with unchanged allocations; the report does not hide this small-fleet result. Shared-host timing is noisy, while the allocation reduction is deterministic evidence of less work. Focused existing group/partition/UI wiring tests and `NewSessionFlow_Golden` passed in the improvement verification. The separate improvement PR should carry its raw before/after receipts and exact final-head checks. Its practical benefit is limited to list rebuilds in those group modes; it does not address the dominant multi-second costs above.

## Coverage and next gates

- The suite uses a disposable HOME, private tmux socket, local prompt scripts and fake SSH. It needs no user credentials, live fleet, model API or paid tool. Isolation tests exercise normal and failed workers plus an unrelated private sentinel. No host repository tests or host-default tmux access are part of this evidence.
- PTY startup measures a populated session row at 120 by 40. Key repeat measures synchronous Update/View with asynchronous commands excluded. Neither is a visual golden comparison of every frame or physical-display latency.
- RSS/fds reuse `health.Start` and `health.Report`, including sampler overhead. They describe the UI harness process, not child shell totals, sustained CPU, production worker counts or leak growth.
- Hook pruning has a seeded fixture. Darwin descriptor scaling, real SSH transport, repeated auth backoff, orderly exit cancellation and actual agent submission need their separate behavioral or platform proofs.
- Default N=3 is reconnaissance. Use at least 11 runs on a quiet machine of the same class before making close threshold decisions. The existing performance budgets still honor `PERF_BUDGET_MULTIPLIER`; fleet comparisons use their explicit p95 threshold and reject missing coverage. CI is advisory first, and this baseline does not turn an already-slow operation into an acceptable absolute budget.
- An earlier host build/vet run passed, but the latest repeat failed because local-toolchain standard-library files were missing. There is no final-head host build/vet PASS claim. The final cleanup/UID source changes still require CI verification.
- Repair Docker's storage/metadata error before claiming a clean end-to-end rerun. Preserve the complete health measurements and partial original measurements independently. Keep the suite and optimization PRs separate, verify their final heads and CI, and park them for the September 25 release without merging or deploying as part of this report.

Run instructions and precise metric boundaries are in [README.md](README.md).
