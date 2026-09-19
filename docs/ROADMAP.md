# Roadmap

This file tracks ideas and follow-up work that are real and worth doing, but are not scheduled for the current release. It exists so the issue tracker reflects what people can actually pick up right now, instead of carrying dozens of parked drafts.

An item here is not a commitment or a queue position; it is a note that the idea was seen, evaluated, and is worth coming back to. When someone (maintainer, contributor, or agent) is ready to start one, it graduates: open a fresh issue linking back to its line here, remove the line (or mark it done), and work proceeds as a normal issue/PR. Priority tags (P1 most, P3 least) are a rough read of user impact and are not a promise of order.

## Status lights and detection

- P2: Status detection for shell sessions leans on a decaying activity timestamp rather than a positive signal, so a silent foreground command can read as idle; consider probing the foreground process (`#{pane_current_command}`) instead.
- P2: Sessions created through `remote add` or `session add` for Codex never get codex-hooks installed automatically, so their status relies on content detection alone; decide between auto-installing hooks at creation time or showing a clear "hooks not installed" marker.
- P2: A status-light audit found six light/substate mismatches worth fixing: Codex shows a usage-limit as a generic error, a stale hook heartbeat still reads as running, an error state does not clear on recovery, the survey/trust/Codex picker states all collapse into one generic "interactive menu," and an ellipsis-plus-token-count combination reads as busy when it is not.

## Remote decks

- P2: The remote-poll status label shows a raw millisecond number above budget without distinguishing a slow poll from a failed one; relabel and re-tune the budget.
- P3: There is no forwarded `session remove` for remote sessions, only archive; add remove/delete forwarding with the same guards as local sessions.
- P3: `remote drain --help` implies an ad-hoc `user@host` target is accepted; only configured remotes are, so the usage text needs fixing.
- P3: An accounts field with live usage limits for the remote preview and header, opt-in, so a fleet's account headroom is visible without switching in.
- P3: Follow-ups from the remote-spawn review: handle tilde- and flag-shaped probe targets, support the fish launch shell, and add an SSH-side check for a world-writable target directory.
- P2: The embedded remote-attach view can freeze when the remote tmux server prints a config warning on start; also forward the skill detach/attached signal.
- P3: Surface the remote tmux config warning once in the status line, and apply the same config-error cancel behavior to local embedded and web attach paths.
- P2: A remote-walk audit found several honesty and forwarding gaps: the version label can be misleading, `null` and `[]` get confused in remote list output, `list --json` and `health` are not fully forwarded to remotes, `-c` does not open the configured shell, and the old-remote dialog only offers a plain shell instead of the real tool list.
- P2: Remote-spawned sessions can fail to find the agent binary because spawn does not include the user's bin directories on a non-login SSH shell; also, the TUI's "remote create" flow drops the row instead of persisting the error record, and remote-created sessions are missing the identity block and `AGENTDECK_*` environment that local sessions get.

## Messaging and inbox

- P3: Add a `--since` cursor to `remote drain` plus ledger pruning, so repeated drains do not re-walk the whole history.
- P3: Enforce one remote-channel owner per machine to avoid two processes racing the same inbox.
- P3: Fold the hook timestamp into the finished-turn identity so two turns that complete in the same window cannot be conflated.
- P3: Have the daemon consume `hooks/<id>.events.jsonl` by offset instead of re-reading the whole file each pass.
- P3: Back off retries for parked run-task completions instead of retrying at a fixed interval.
- P3: Make the `conductor_bridge` queue file-backed, or retire it if nothing depends on the in-memory version.
- P3: The conductor's status, heartbeat, and log surfaces can disagree about the same event; reconcile them to one source of truth.
- P3: Messaging-review follow-ups: scope the drain gate to the scratch session's config dir, HTML-escape rewritten content, avoid duplicate entries on hook blocks with the same matcher, and require every event type to be present before treating a session as caught up.
- P2: A messaging audit found several reliability gaps worth closing: hooks should call the agent binary by absolute path, `[DONE]` sentinels should be detected under per-account config directories, the inbox consumer should take a file lock before draining, the dead-letter liveness gate should be removed, an async-hook drift guard is needed, sends to the same target need a per-target lock, and the TTL sweep should exclude the `_unowned` bucket.

## TUI rendering

- P3: The New Session model row is hard to browse on a long list (arrow scrolls forever, feels stuck); add type-to-filter, a bounded list with common models first, and Escape to leave the row. Apply the same fix to the remote dialog.
- P3: Pressing `m` (MCP manager) on a pi/other session that does not support it does nothing with no feedback; show a one-line notice instead.
- P2: Optional header fields (like accounts) can render at the far right and get cut off; use a compact form and a width-aware layout that drops the lowest-priority field instead of truncating mid-text.
- P2: The notification bar can treat a teammate's attach as "the user is looking at it," because the attached-session lookup returns the first session with any non-control client; a shared session's own events then get dropped from the bar while only the teammate is viewing it.
- P2: The status-left notification bar and the Ctrl+Q root binding are installed server-global in tmux, so a teammate who attaches with plain `tmux attach` to the same server inherits them; scope both per session/client.
- P3: Two people typing into one shared session at once can flip the window size on every keystroke under "latest" sizing; consider a short debounce or a per-session "driver" choice.

## Sessions and harnesses

- P2: `session send --wait` can report "session transcript not found before the timeout deadline" even though the message was delivered and answered; the wait path should fall back to pane-based completion detection instead of failing the whole send.
- P2: A superseded session ("Restart with new session ID") leaves its old tmux session running forever with nothing tearing it down; decide the lifecycle — tear down on supersede, or list it under doctor/health with an explicit stop verb.
- P3: Heartbeat's picker detector can be thrown off by option descriptions or wrapped lines, and the "esc to interrupt" spinner can mask an open picker underneath it; also a stray Python test class sits after the `__main__` guard in the bridge suite.
- P2: The usage-ingest wrapper's bare-form statusLine strips an inherited `AGENTDECK_PROFILE` from the wrapped command's environment; pass the caller's environment through unchanged except agent-deck's own variables.
- P3: The tmux window-policy hook has no config opt-out for installing the global after-new-window hook; add one, make uninstall also drop per-session options, make the ownership-signature match and accepted values consistent, and add `--json` parity to `tmux-hooks`.
- P2: A round of small verification gaps to close: the creds-refresh dispatcher's help-vs-consent handling, a colour-wiring issue, a transcript check that should run before `-c`, help/retry/purge text for dead letters, and missing test coverage for a few already-fixed issues.
- P2: The launch/send truncation guard counts wrapped display lines as separate message lines, so it can refuse a perfectly valid one-line prompt just because it wraps on screen; this is a regression from an earlier truncation fix and needs its own fix.
- P2: A second round of verification gaps: the shell-tool fallback path on a poisoned server, re-applying dead-letter retry/purge/TUI consistently, applying requested workflow revisions to the structural-quality-gate PR, and a trailing-newline floor plus `session send --message-file` parity for the paste-marker guard.
- P2: A bundle of small follow-ups worth doing together: making the health warning sustained-only, fixing the drain `--help` wording, adding `binary_version` to health, and adding a skills hint plus host line to the identity block.

## Health, metrics and performance

- P2: The footer budget warning can fire on the very first status pass of a fresh deck with 150+ sessions (one slow pass, then fast every time after); require a sustained breach (three consecutive) or scale the budget by session count instead of firing on one sample.
- P3: The auth-failure refresh path got measurably slower (p95 roughly 1.7ms to 5.1ms) after the non-blocking poll rework; still well under a millisecond-scale budget, but worth a look at the call shape.
- P2: A per-session event journal and `session metrics` command (turns, send acknowledgements, restarts, dead letters), for both local and remote sessions, to give evals something real to read instead of guessing from logs.
- P3: Health samples carry a record schema version but not the binary version, so a mixed-version deck is invisible in `health --json`; add `binary_version`.
- P1: The TUI can burn a lot of idle CPU with a large fleet (around a quarter on average, spiking well past 100% with 100 sessions and 4 remotes), and one remote's poll pass has been seen taking well over its budget with 50+ sessions; profile the status pass and the remote poll and bring idle CPU down substantially.

## Update and install

- P2: `update --timer-status` / `--check --json` can report the update timer as not installed even when a working legacy-named timer unit is active on the host; detect the legacy unit name, or migrate it during `--install-timer`, so the fleet's real update coverage is visible.
- P2: `remote list --check` should show whether each configured remote has an update timer installed, and offer `remote update --install-timer`; at least one remote has been found relying only on the controller's sweep with no local timer at all.

## CI and repo

- P3: Bring the bundled skills up to date with the current release and add skill-creator evals so they do not drift again.
- P3: Add a CHANGELOG mention for the creds-refresh consent fix, a CI lint test for the Dependabot auto-merge workflow, and wire the structure-advisory lint test into an actual workflow job.
- P2: The conductor bridge's Python test suite has failing tests when run locally (bridge path/proxy/attribution tests) because it assumes things about the local network/proxy/environment that are not always true; make the suite hermetic and add it to CI so it cannot silently rot.

## Security and signing

- P1: **Signed macOS release binary.** The release binary is ad-hoc signed; on macOS 26 this makes system security daemons re-scan it on every launch, pinning CPU and draining battery on affected Macs. The real fix is a Developer ID signed and notarized binary in the release pipeline, which needs an Apple Developer account wired into the release machine. Workarounds in the meantime: install under `/usr/local/bin` via Homebrew, or exclude the binary in the security tool's settings.

## Later ideas

- P2: **GitHub event intake.** An opt-in poller/dispatcher so the conductor wakes on new PRs/issues instead of relying on a maintainer noticing them manually. A prior version of this was retired because every extra long-running process is another thing that can leak, rebind, or drift; a good first step would be a bounded `agent-deck github-events` command that only prints new events since a cursor, without running as a daemon.
- P3: **Structural quality gate.** An advisory-only CI check (using the `sentrux` tool) and a worker MCP entry to flag structural code-quality regressions — god files, import cycles, overly complex functions — without failing builds.
- P3: **Acceptance harness for the context inspector.** A large acceptance-test harness (oracle/driver/testimony tooling) was built to validate the context-inspector feature before that feature had landed. The context inspector has since shipped with its own tests and golden frames, so the harness would need a full rewrite against the final shape; whatever is still useful from its design is worth revisiting once someone picks this back up.
- P2: **Go 1.26 toolchain.** A dependency-group bump pulled in modules that require `go 1.26`, but CI is still pinned to Go 1.25. Once Go 1.26 is reliably available in CI, bump the pinned toolchain version and let the dependency group land.
- P3: **Native Oh My Pi (`omp`) harness support.** Tool detection, TUI presets, launch flags, status tracking, and restart/fork lifecycle for the `omp` agent harness, so it stops falling back to the generic shell tool. Already has a working implementation in flight; landing depends on syncing that work with the rest of the tool registry as it evolves.

---

Have an idea that is not here? Open an issue as usual — this file is for work that has already been triaged and set aside, not a replacement for the tracker.
