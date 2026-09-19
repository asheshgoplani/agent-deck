# Synthetic fleet benchmarks

Run the complete suite with one command:

```sh
make bench-fleet BENCH_ARGS='-runs 3 -machine my-linux-class -out bench/run.json'
```

The first invocation builds a Go 1.25 image with tmux and downloads modules. Subsequent invocations reuse the image layers and compiler cache. Networking is enabled only while building dependencies. The measurement container runs as the invoking nonroot UID and GID so bind-mounted reports are writable, with no network or capabilities, and only the checkout and a named compiler cache mounted. Its HOME is disposable. Do not run repository tests on a live maintainer host.

The existing `make bench` advisory microbenchmarks and `make test-perf` budget tests remain intact. `PERF_BUDGET_MULTIPLIER` still controls those existing tests; it does not scale measured fleet values. The fleet comparison uses an explicit fractional threshold:

```sh
go run ./tools/bench compare -baseline bench/baseline/linux-arm64.json -run bench/run.json -threshold 0.25
```

Comparison requires identical platform, machine class, seed, sample count, metric names and units. It fails for missing coverage and for a p95 increase above the threshold. A zero baseline permits only zero. Never relabel a different machine to make comparison pass. Use at least 11 runs for decisions close to a threshold. With the default three runs, nearest-rank p95 is the maximum, useful for quick reconnaissance rather than a precise tail estimate.

## Isolation

Each fleet gets a new scratch directory, HOME, TMUX_TMPDIR and private `-L fleet` socket. A PATH shim forces every product tmux subprocess onto that socket and counts invocations. Cleanup calls the original tmux binary with the same socket name and directory, then verifies the server is gone. Neither the inherited TMUX variable nor the host default server is used. Scratch files are retained for inspection until the container exits. SIGINT/SIGTERM trigger cleanup; forced termination is contained by the disposable container.

The synthetic fleet contains persisted Claude, Codex and shell metadata in equal rotation. Claude and Codex panes run local scripts with fixed prompt fixtures and synthetic persisted bindings; the shell panes execute local commands. The suite checks the observed tool mix after listing, so runtime detection cannot silently turn the fleet into shells. No model API, credential, personal transcript or paid tool is used. Send latency includes the product CLI delivering a shell command and polling for a distinct complete acknowledgement line. A shell has no agent composer, so the high-level CLI may report submission unconfirmed even when the shell executes the command. The separate `send_cli_unconfirmed` count preserves that verdict; the suite requires the executed acknowledgement and rejects other CLI errors. This does not establish agent submission acceptance or measure a model response.

Hooks are installed only into the disposable Claude configuration before timing. Each startup receives three old orphan hook files per session, and the suite records how many remain after the first populated frame. This exercises pruning but Linux inotify cannot establish Darwin kqueue descriptor scaling.

Three fake SSH hosts use the production SSH runner: immediate JSON success, success after six seconds, and authentication failure. Direct refresh measurements cover request execution. The same remotes are configured during the PTY startup measurement. Retry backoff, orderly exit cancellation and real network behavior require the remote worker's separate behavioral tests.

## Measurement boundaries

JSON stores raw samples, nearest-rank p50/p95, fixture seed, platform, machine class and source revision. Markdown contains the same table. Timings exclude compilation and fleet creation. CLI timings include process startup. Tmux subprocess counts include instrumented native commands only, not every descendant process.

The UI harness loads the persisted fleet through the production model and measures a synchronous status pass plus Update/View under repeated navigation. Returned asynchronous commands are outside that render measurement. The first populated model frame excludes process startup and terminal painting. `cold_start_populated_terminal_frame_ms` separately measures process spawn until a synthetic session title reaches a 120 by 40 PTY, including production startup workers and configured remotes; child cleanup is excluded. `group_activity_map_ms` is the mean of 100 direct calls per run on the seeded nested group tree. Resource readings describe the harness process with test-disabled background workers, not a long-lived production TUI.

See REPORT.md for measured costs, limitations and the separate optimization evidence. Baselines must retain their original machine class and source revision.

## Shared runtime health dependency

The suite PR is stacked on runtime health (#2289), which in turn includes the status-loop fix (#2290). The UI resource probe calls the existing `health.Start` and `health.Report` APIs, preserving `rss_bytes`, `open_fds` and nullable unsupported values. There is no second platform sampler. Original-main measurements use the same driver with a pre-health UI harness and explicitly mark resource readings unavailable.

## Advisory CI policy

Measurement, isolation or build failures fail the job. Comparison failures keep
their nonzero outcome and regression rows, emit a workflow warning and add an
explicit failed-comparison summary, but do not fail the advisory job. A green
job therefore proves measurement execution, not performance acceptance.

Two unchanged-head three-sample runs flagged different non-group metrics,
including startup outliers and overlapping SSH counts. Preserve these failures
and calibrate sampling/variance before removing comparison `continue-on-error`
to make it required. Do not widen the comparison threshold to erase them.
