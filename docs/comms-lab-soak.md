# Real-agent comms lab soak

`scripts/comms-lab-soak.py` is an explicit opt-in **capture rig**, with a 30-minute measurement window after startup. It starts one throwaway Claude conductor and 28 real interactive children: Claude, Codex, Gemini, Pi, Hermes, OpenCode and Cursor on each of the Mac and a dedicated SSH lab host, separately for urgent and info scenarios. It never uses Claude print mode. Real-model execution can incur charges.

Running the script without `--run` prints issue #2482 targets and starts nothing. The CI fake-harness matrix does not need provider credentials and must never pass `--run`.

## Prepare dedicated lab credentials

The conductor operator must provision two new short canonical directories, such as `/private/tmp/comms-lab-20261004` on macOS and `/tmp/comms-lab-20261004` on the remote. Unix socket path limits make a short root necessary. Each directory must contain:

* A `LAB-ONLY` file containing exactly `disposable-comms-lab`.
* A `home/` directory with independently provisioned lab authentication for all seven CLIs. No symlinks to live configuration or copied agent-deck state. Credentials remain in these isolated homes; the runner does not read real HOME config or inherit API credentials.
* On the Mac, `home/.ssh/config` with the dedicated lab host, dedicated key and known-hosts file, `BatchMode yes`, `StrictHostKeyChecking yes`, `ControlMaster no`, `IdentityAgent none`, `IdentitiesOnly yes` and explicit absolute paths inside this disposable home. Do not use `Include`, proxy commands, external key paths or live SSH control sockets. Ensure the remote host alias resolves using this file.

Install the two reviewed agent-deck binaries and seven authenticated harness CLIs beforehand. Use an absolute binary path and a `--tool-path` valid on both hosts. All writable runtime roots (`deck`, `config`, `cache`, `data`, `state`, `runtime`, `tmp`, `work`, `bin`) must be absent: preparation creates them exclusively and refuses reuse. Do not configure external messaging integrations. The operator must ensure harness authentication works in the isolated homes before the run; interactive login or trust prompts are captured as failed workload, not bypassed.

Example, **only in the dedicated lab**:

```sh
python3 scripts/comms-lab-soak.py --run \
  --root /private/tmp/comms-lab-20261004 \
  --remote-root /tmp/comms-lab-20261004 \
  --remote comms-lab-host \
  --ssh-config /private/tmp/comms-lab-20261004/home/.ssh/config \
  --binary /opt/comms-lab/bin/agent-deck \
  --remote-binary /opt/comms-lab/bin/agent-deck \
  --tool-path /opt/comms-lab/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
```

A private `bin/agent-deck` shim pins hook calls to the reviewed binary while preserving harness identity. The remote SSH entry wrapper clears inherited environment and sets the same isolated paths. The driver launches a title-recognized conductor role directly. It deliberately avoids `conductor setup`, which can install machine-wide launchd services. Each host gets its own foreground notify-daemon in an owned tmux pane. Both agent-deck and direct tmux commands use socket name `comms-lab` under the same isolated `TMUX_TMPDIR`. Cleanup enumerates exact pane IDs on that socket and closes those panes only; it never issues `kill-server`, session-name matching or commands against the default socket. Roots and evidence remain for inspection. Interrupts run cleanup; host crashes or an unreachable remote can leave owned lab panes requiring operator cleanup using the recorded root/socket.

## What is exercised and what remains unknown

Every five minutes the driver sends a unique `COMMS_LAB` task to every child. Urgent children answer a simple parent task. Info children are instructed to launch native background/async work and process its actual completion notification. A harness without that facility must return `UNSUPPORTED_BACKGROUND`; asking for an info scenario does not prove an info record was produced. The driver does not fabricate task-notification text or hook payloads. Local urgent sends carry the conductor identity through `AGENTDECK_INSTANCE_ID`, exercising the actual sender-envelope classification. Remote parent IDs cannot cross machines in current main, so remote children are unparented and the real `remote drain r1 --into <conductor-id>` path ingests their export. This limitation is recorded and is not equivalent to a cross-host parent-envelope test.

Every 30 seconds, bounded public CLI calls collect `msg export --json`, `inbox stats --json --all`, `health --json`, `list --json`, `session metrics --json --all` and `recall search COMMS_LAB --json` on both hosts. Unsupported commands, including future `msg export` on older binaries, preserve nonzero exit and stderr. The driver does not replace unsupported contracts with reads of internal ledger files. The `recall` command runs on the actual remote, so missing indexing or transcript evidence remains visible.

`receipts.json` contains each command, host, timestamp, transport exit, command exit and output. `report.json` prints all issue targets with null/UNKNOWN for unmeasured counters. If a supported local export returns recognizable records, the report includes numerator and denominator for text coverage and sender/text coverage, and a count of remote records with all three latency timestamps. It reports each observed send's sender, text/hash presence and final delivery state (joining delivery.ref to send.id and checking SHA256 text hashes), plus cell tier/trigger counts. At the end, it locates the conductor's isolated native Claude JSONL transcript via the public session id, deduplicates assistant usage by message id, and reports input, output, cache-read and cache-creation tokens. These are whole-conductor-session totals, including startup, not tokens per machine wake: the wake denominator remains unknown. Missing transcripts remain unknown. It does not infer timestamps from capture time, treat sends as finished events, extrapolate notifier CPU/day, or infer tokens from response length. Retain the isolated homes' transcript, health and debug journals for independent review; these may contain sensitive model content or credentials and must not be uploaded wholesale.

This driver attempts urgent and native background/info scenarios, **not the complete lab acceptance matrix**. Actual background support must be verified per harness. Blocked composer and approval flows, siblings, human Telegram delivery, independently counted finished events, per-wake token analysis and notifier CPU need dedicated instrumentation/cases. It never sends Telegram or email. Transcript/Journals must establish the remaining targets before a release can pass, or the PR must explicitly waive each gap. A successful capture returns exit **2 (HOLD)**; setup/runtime failures return **1**; plan-only returns **0**. No run automatically certifies issue #2482.
