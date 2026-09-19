# Runtime health

The TUI, `web --no-tui`, and `notify-daemon` sample their own runtime once a minute and on startup/shutdown. Samples stay on the machine in the selected profile's `logs/health` directory. They contain resource counts and timings, never session transcripts. Ordinary CLI commands do not start a sampler.

```toml
[health]
enabled = false        # default: true; restart long-lived processes after changing
session_events = false # default: true; the per-session event journal `session metrics` reads
```

```sh
agent-deck health
agent-deck -p work health --json --since 30m
agent-deck doctor --json
agent-deck remote exec office health --json --since 1h
```

Remote exec runs the same command on the configured owner host, using that remote's profile and returning the same JSON schema. It requires a remote binary with the health command.

Each process is identified by role, PID and start time, so PID reuse does not merge unrelated history. Reports include latest observations, min/p50/max within the requested window, and warnings. Missing observations are `null`, never zero. Samples older than two minutes are marked stale; recent samples alone do not prove that a process remains alive.

Each sample carries `binary_version`, the running binary's version string, alongside the record schema `version`, so a mixed deck (e.g. a partially updated remote) is visible in `health --json` and `doctor` rather than silently mixed into one series. Samples written before this field existed simply omit it; it decodes as empty, never a fabricated value.

Default budgets are status pass under 250 ms, fewer than 512 open descriptors, at most twice as many tmux subprocess starts as sessions, and remote poll under two seconds. CPU percent uses one core as 100%, so a multithreaded process can exceed 100%. Tmux counts measure process-wide starts during the status-pass interval and can include overlapping background work. Hook counts cover the shared hooks directory; more than 4096 entries is reported as unknown to bound sampler work. Timing observations refer to operations measured during the sampling interval; missing operations remain unknown.

Files rotate at 1 MiB with one backup and age out after seven days while a sampler runs. Cleanup retains the newest 128 files plus files modified in the last two minutes, so active writers are not removed. Health collection errors do not interrupt the application; missing data remains visible as unknown in the report. Disabling collection leaves existing history available.

The tagged headless regression runs the status path against 100 fake sessions in both disconnected and active Codex fleets. The Codex fixture primes discovery caches as a running TUI would, then asserts that the measured pass still synchronizes metadata and retains every session identity:

```sh
go test -tags runtimehealthperf ./internal/ui -run TestPerf_RuntimeHealth -count=1 -v
```

Run tests inside Docker as required by the repository development workflow. `PERF_BUDGET_MULTIPLIER` scales timing budgets for shared runners. Synthetic sessions exercise the local status path, not live SSH latency or production-scale filesystem histories.

## Session metrics

Next to the process samples, the transition daemon, the CLI send/restart/stop paths and the task-worker wrapper append per-session events to `logs/health/sessions-YYYYMMDD.jsonl` in the same directory (one file per UTC day, the same 1 MiB cap, backup and seven-day retention). `agent-deck session metrics <id> --json` derives turns, turn duration, waiting time, send outcomes and ack times, restarts, dead letters and worker completion time from it; `agent-deck health --json` carries the profile roll-up under `sessions`. `[health] session_events = false` stops the writers; readers then report `journal: disabled`. See the CLI reference for the field table and how to read the numbers.
