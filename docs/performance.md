# Performance

How fast agent-deck is, how we measure it, and how you can check it on your
own machine.

## What is measured and why

Users feel speed in a few places: the TUI opening, moving through a long list,
attaching to a session and coming back, sending a message, and seeing a status
change. Each of these is an operation with a name, a start point, an end point
and a target. We time the operation the user sees, not a helper inside it.

Targets are p95: 19 out of 20 runs should be at or under the target. The
catalogue lives in code (`internal/optiming/summary.go`, `Targets`), so
`agent-deck perf` and this page use the same names.

## Operation catalogue

| Operation | Name | Target (p95) | Span |
|---|---|---|---|
| TUI start | `tui.first_frame` | 500 ms | process start to the first frame with the session list |
| List render | `BenchmarkOp_TUIListRender/{100,500}` | 16 / 33 ms | one full frame with 100 or 500 sessions |
| Attach | `tui.attach` | 300 ms | Enter on a session to the first byte of the live pane |
| Detach | `tui.detach` | 150 ms | detach key to the session list drawn again |
| Preview | `tui.preview` | 150 ms | preview fetch for the selected session, after the 150 ms debounce |
| Status sweep | `tui.status_pass` | 250 ms | one status pass over every session |
| Search | `BenchmarkOp_TUISearch/500` | 16 ms | filter 500 sessions by a query |
| Notify daemon pass | `daemon.pass` | 100 ms | reload sessions, refresh tmux, emit transitions |
| Remote list | `remote.list` | 2 s | list the sessions of one ssh remote |
| Remote attach | `remote.attach` | none (visit length) | ssh remote attach until the remote client exits; the whole visit, so no speed target |
| Web menu | `web.menu` | 100 ms | web UI session menu request served |
| Update check | `update.check` | 2 s | release check against GitHub |
| Update install | `update.install` | 30 s | download, verify and install a release |
| `agent-deck list` | `cli.list` | 300 ms | process start to exit |
| `agent-deck status` | `cli.status` | 300 ms | process start to exit |
| `agent-deck events publish` | `cli.events.publish` | 100 ms | one call |
| `agent-deck session send` | `cli.session.send` | 3 s | includes the wait for a ready agent |
| `agent-deck session start` | `cli.session.start` | 2 s | tmux session created, agent launching |
| `agent-deck launch` | `cli.launch` | 3 s | add, start and the optional first message |
| Any other command | `cli.<cmd>[.<sub>]` | 500 ms | process start to exit |

The macOS app has its own budgets (main-thread stalls, switch, streaming,
first paint). They are checked in the Mac app's lab runs, not here.

## How each number is measured

There are four sources. Each number on this page says which one it came from.

1. **Operation benchmarks** (`BenchmarkOp_*`). Each one builds an N-session
   fixture in a sandbox: its own `HOME`, its own state database and its own
   tmux socket. It never touches your real sessions. Maintainers run them in a
   locked-down container (no network, fixed CPU count), so runs on one machine
   compare fairly.
2. **Live timings.** Set `AGENTDECK_TIMINGS=1` (or `[logs] timings = true` in
   `config.toml`). Every agent-deck process (CLI, TUI, web server, notify
   daemon) then appends one line per finished operation to `timings.jsonl` in
   the cache directory (`~/.cache/agent-deck/` by default). `agent-deck perf`
   prints count, p50, p95 and max per operation against its target. Off by
   default. When off, each timing point costs one atomic load and no
   allocation.
3. **Budget tests** (`TestPerf_*`). Hard CI gates on cold start, `list` with
   100 sessions, the state database, the conductor outbox and group
   create/delete. Rules and budgets: [perf-budget-suite.md](perf-budget-suite.md).
   Run them with `make test-perf`.
4. **Fleet bench** (`make bench-fleet`). Docker runs with 10, 100 and 500 real
   tmux sessions and fake ssh hosts. Good for status pass, `list --json`,
   send round trip and TUI start in a real terminal. See
   [bench/README.md](../bench/README.md).

## Run the benchmarks yourself

Record a results file for a git ref:

```
make bench-ops
# or
bench/ops/run.sh <label> <ref>
```

On a normal machine without the maintainers' container, run the benchmarks
directly:

```
go test -tags opbench -run '^$' -bench '^BenchmarkOp_' -benchmem -count=5 ./...
```

Use `-count=5` or more and compare runs from the same machine. Numbers from a
laptop on battery or a busy shared host are noisy.

To see live numbers from your own use:

```
export AGENTDECK_TIMINGS=1
agent-deck            # use it as normal for a while
agent-deck perf       # or: agent-deck perf --json --since 1h
```

## Privacy

Everything stays on your machine. Timings are written only to the local
`timings.jsonl` (rotated at 8 MiB). Each line holds the operation name, its
duration in ms, whether it failed, the process id and the time. No session
titles, paths, prompts or output. Nothing is uploaded, and telemetry does not
send operation timings. Delete the file at any time.

## Recording numbers per release

Each release gets one results file in `bench/ops/results/`, made with
`bench/ops/run.sh` on the release tag. The tables below are generated from the
latest file. Do not edit between the markers by hand; rerun the script.

## Current numbers

<!-- BENCH:BEGIN -->
Generated by `tools/benchops` from `bench/ops/results/*.json`. Median ms per operation over the -count runs; newest: **1.16.33-dev** (973275ec, Linux x86_64 (i9-13900), deck-test container: 6 CPU, 12 GB, shared host).

| Operation | Sessions | Target | 1.16.31 | integration-20261019b | 1.16.33-dev | Δ newest | Verdict |
|---|---:|---:|---:|---:|---:|---:|---|
| TUI cold start to first frame (`tui.first_frame`) | 100 | 500 ms | 22.7 | 22.0 | 20.9 | -5% | ok |
| TUI cold start to first frame (`tui.first_frame`) | 500 | 500 ms | 77.1 | 77.6 | 83.2 | +7% | ok |
| TUI list render (`tui.list_render`) | 100 | 16 ms | 0.840 | 0.832 | 0.913 | +10% | ok |
| TUI list render (`tui.list_render`) | 500 | 33 ms | 0.836 | 0.947 | 0.960 | +1% | ok |
| TUI cursor move (`tui.key_frame`) | 100 | 16 ms | 0.994 | 1.22 | 1.25 | +3% | ok |
| TUI cursor move (`tui.key_frame`) | 500 | 16 ms | 0.978 | 0.996 | 1.22 | +23% | ok |
| Search / filter (`tui.search`) | 500 | 16 ms | 0.157 | 0.160 | 0.186 | +16% | ok |
| Status sweep (`tui.status_pass`) | 10 | 250 ms | 8.26 | 9.30 | 7.34 | -21% | ok |
| Status sweep (`tui.status_pass`) | 50 | 250 ms | 12.7 | 13.8 | 14.4 | +5% | ok |
| Session store load (`statedb.load`) | 100 | 50 ms | 10.1 | 9.94 | 9.82 | -1% | ok |
| Session store load (`statedb.load`) | 500 | 50 ms | 36.7 | 36.5 | 34.9 | -4% | ok |
| CLI cold start (`cli.version`) | - | 50 ms | 11.4 | 11.2 | 11.1 | -1% | ok |
| CLI list (`cli.list`) | 100 | 300 ms | 124 | 142 | 106 | -25% | ok |
| CLI list (`cli.list`) | 500 | 300 ms | 623 | 424 | 459 | +8% | **over** (1.5x) |
| Session add (`cli.add`) | - | 500 ms | 43.4 | 44.0 | 46.8 | +6% | ok |
| Session start (`cli.session.start`) | - | 2000 ms | 60.1 | 58.6 | 71.0 | +21% | ok |
| Send (`cli.session.send`) | - | 3000 ms | 137 | 134 | 132 | -2% | ok |
| Session stop (`cli.session.stop`) | - | 500 ms | 58.1 | 43.5 | 42.6 | -2% | ok |
| Events publish (`cli.events.publish`) | - | 100 ms | 1019 | 1015 | 1014 | -0% | **over** (10.1x) |
| Web menu (`web.menu`) | 100 | 100 ms | 867 | 1241 | 1029 | -17% | **over** (10.3x) |
| Web menu (`web.menu`) | 500 | 100 ms | 19053 | 17872 | 19294 | +8% | **over** (192.9x) |
| Timing hook cost (off) (`optiming.hook_off`) | - | 10.0 ns | n/a | n/a | 2.0 ns | n/a | ok |
| Timing Since cost (off) (`optiming.since_off`) | - | 10.0 ns | n/a | n/a | 1.0 ns | n/a | ok |
<!-- BENCH:END -->

## Slowest operations vs target

Operations whose benchmark median is furthest over target, worst first.
This is the backlog for speed work.

<!-- BACKLOG:BEGIN -->
Operations over target on **1.16.33-dev**, worst first by median / target, one line per operation (generated).

1. **Web menu** (`web.menu`, 500 sessions): 19294 ms vs 100 ms target, 192.9x. Every request reloads state.db and runs a full, unthrottled status pass over all sessions (refreshStatuses, internal/web/session_data_service.go); cost grows faster than N. Claude rows without fresh hook data are forced to a full pane check (ForceNextStatusCheck) on every request.
2. **Events publish** (`cli.events.publish`): 1014 ms vs 100 ms target, 10.1x. Bus.Flush waits for the writer's 1 s sync ticker (internal/events/bus.go), so every call takes about 1 s; fix: sync on Flush or Close instead of waiting for the tick.
3. **CLI list** (`cli.list`, 500 sessions): 459 ms vs 300 ms target, 1.5x. Results recorded before the stand-in fix (1.16.31, integration-20261019b, 1.16.33-dev) are inflated: the tmux stand-in answered only the first command of the batched pane capture (tmux.PrimePaneCaptures, #2762), so every session paid its own capture-pane fork (about 0.9 ms per session, linear in N). With the fixed stand-in: about 37 ms at 100 and 106 ms at 500 (integration 26cb141e); real tmux with 500 live sessions lists in about 0.2 s with 12 tmux processes. Rerun bench/ops/run.sh to refresh the stored results. The fleet bench numbers (475 ms at 100, 9.7 s at 500) are from 6574d29, before the batched capture.
<!-- BACKLOG:END -->

Known slow spots that the benchmarks above do not cover yet:

- **Notify daemon CPU.** Owner fleet reports about a third of a core. Each
  pass (every 1 s while a session runs) reloads every session from state.db
  and refreshes tmux for every profile (`internal/session/transition_daemon.go`).
  With 3 sessions a pass takes 24 ms p50 and the daemon uses 0.3% of a core,
  so the cost grows with the fleet. `daemon.pass` in `agent-deck perf` shows it
  on a real fleet.
- **Conductor heartbeat.** `heartbeat-tick` refreshes the status of the whole
  fleet, then calls `UpdateStatus()` again for each child, so the status work
  runs twice per tick (`cmd/agent-deck/conductor_heartbeat_tick_cmd.go`).

## Live numbers from one real TUI run

The real TUI in a sandbox HOME, its own tmux socket, 3 shell sessions,
`[logs] timings = true`, then `agent-deck perf` (2026-10-20, same machine as
above, not a benchmark):

| Operation | Runs | p50 | p95 | Target |
|---|---:|---:|---:|---:|
| `tui.first_frame` | 1 | 25 ms | 25 ms | 500 ms |
| `tui.attach` | 2 | 74 ms | 82 ms | 300 ms |
| `tui.detach` | 2 | 6.9 ms | 7.2 ms | 150 ms |
| `tui.preview` | 14 | 2.0 ms | 10 ms | 150 ms |
| `tui.status_pass` | 12 | 16 ms | 41 ms | 250 ms |
| `daemon.pass` | 10 | 24 ms | 30 ms | 100 ms |
| `cli.events.publish` | 1 | 1.0 s | 1.0 s | 100 ms |
