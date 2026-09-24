# TUI large-list stress diagnostics, 2026-09-24

## Coverage

The requested Cartesian matrix has 72 cells per machine: four session counts, three state mixes, three group layouts, and two remote configurations. This run does **not** establish that matrix. The table below is a matched diagnostic using the repository's existing private-fleet harness. It uses a mixed shell, Claude, and Codex fleet with real local tmux panes, nested groups, three fake remotes, and no continuous hook firing. It is not a measurement of the all-stopped, half-running, all-running-with-hooks, flat, 20-group, or 100-cached-remote cases.

The build Mac could not create the 600-pane fixture. Its private tmux server returned `create window failed: fork failed: Device not configured` at pane 509, after raising only the test shell's soft file descriptor limit to 4096. The harness verified teardown through the same private socket. That result is a fixture limit, not a 600-session performance measurement.

| Machine | Requested cells measured | Mixed diagnostic sizes | Missing metrics |
|---|---:|---|---|
| buildmac | 0/72 | 50, 150, 300 | TUI CPU%, daemon CPU%, live TUI RSS |
| g14 | 0/72 | Pending isolated mixed benchmark | TUI CPU%, daemon CPU%, live TUI RSS |

## Build Mac mixed-fleet measurements

These values came from the same seeded fixture and three samples per size. Key redraw has 300 samples per size because each harness run traverses 100 rows. `status_pass_ms` is the production periodic status sweep called from the UI harness. RSS belongs to that test process; the harness does not keep a live TUI or notification daemon running for a steady-state CPU/RSS sample. Times are milliseconds. RSS is MiB.

| Sessions | Metric | Before p50 | Before p95 | After p50 | After p95 |
|---:|---|---:|---:|---:|---:|
| 50 | Key to redraw, ms | 0.944 | 1.292 | 0.901 | 1.048 |
| 50 | Status tick, ms | 477.694 | 555.986 | 302.169 | 405.295 |
| 50 | tmux subprocesses per tick | 119 | 121 | 83 | 85 |
| 50 | UI harness RSS, MiB | 53.25 | 54.89 | 51.95 | 52.16 |
| 150 | Key to redraw, ms | 0.963 | 1.218 | 0.929 | 1.081 |
| 150 | Status tick, ms | 987.288 | 1206.331 | 308.501 | 318.309 |
| 150 | tmux subprocesses per tick | 320 | 322 | 83 | 85 |
| 150 | UI harness RSS, MiB | 60.03 | 60.30 | 56.05 | 56.11 |
| 300 | Key to redraw, ms | 0.964 | 1.112 | 0.943 | 1.091 |
| 300 | Status tick, ms | 1729.662 | 1802.271 | 329.969 | 337.281 |
| 300 | tmux subprocesses per tick | 620 | 622 | 83 | 85 |
| 300 | UI harness RSS, MiB | 69.05 | 69.27 | 62.98 | 63.16 |
| 600 | Private tmux fixture | Failed at pane 509 | Unmeasured | Unmeasured | Unmeasured |

The test fixture exercises `internal/ui/fleet_bench_test.go` with a production `Home` model and the repository's `tools/bench` runner. The key metric times `Home.Update(tea.KeyMsg)` plus `Home.View()`, excluding terminal paint and asynchronous command execution. The tick metric times `Home.backgroundStatusUpdate()`. The benchmark creates a new sandbox HOME and `TMUX_TMPDIR` plus a private `fleet` socket per size and phase, and verifies that server's teardown. The three remotes are fake SSH commands supplied by the harness; this does not measure a live remote or 100 cached rows.

Raw build Mac reports: `../../artifacts/before-mixed-recovered.json` and `../../artifacts/after-mixed.json` from the task workspace. The before report was reconstructed from the harness's retained successful 50, 150, and 300 per-size `metrics.json` files after its 600-size run failed. The after report is the harness's direct output. Benchmark source revision: before `36d7a14b6`, after `e13b02446` (the later commits only add diagnostic tooling and docs).

## Root cause and fix

1. `internal/session/instance.go:6253` held `Instance.mu` while `tmux.Session.Exists()` could wait for a missing-session probe. Any concurrent status reader, including UI and app consumers of the shared instance, waited for that probe. `probeTmuxExists` now releases the lock for the external call and rejects a result that would overwrite a concurrent stop.
2. `internal/session/codex_exclusion_cache.go:57` read `CODEX_SESSION_ID` with one `tmux show-environment` subprocess per session on each ownership refresh. `internal/tmux/tmux.go:7150` now reads session names and their environment values in one `list-sessions` format query. This was verified on private sockets on G14, the local Mac, and buildmac.
3. `internal/ui/home.go:6283` polled every tracked session on a periodic pass. It now rotates through at most 32 candidates per pass; hook and pipe events still refresh active sessions. The old 100-session focused regression updated all 100 on one tick. The new test verifies no more than 32 on the first tick and all 100 after four ticks.
4. `internal/ui/home.go:3715` repeatedly recounted the remaining rows while jumping to the end of an embedded sidebar. It now subtracts and adds each row height once. The G14 synthetic 300-session jump changed from 660,344 ns/op to 12,395 ns/op in committed-head runs. That was a scaling defect, although the measured submillisecond baseline at 300 does not by itself explain the reported multi-second freeze.

## Remaining gates

The requested full state/group/remote matrix and live TUI/daemon CPU measurements remain unmeasured. The repository's visual gallery also has an inherited mismatch: both `36d7a14b6` and the performance branch differ from the committed `01-list` golden at all three widths before later frames can be captured. The actual base and branch first frames are identical. The golden expects 11 sessions and error glyphs where both builds render 10 sessions and idle glyphs. No golden was changed.

`bin/stress-matrix.sh` regenerates this **mixed-fleet diagnostic slice** from prebuilt CLI, UI harness, and benchmark binaries on G14 or buildmac. It does not claim to generate the missing Cartesian cases.
