# The tmux/eval reaper (maintainer hosts only)

`scripts/reap-stale-tmux.sh` cleans up what leaks out of the test suite on a
machine that runs it a lot:

- **test tmux servers.** A leaked server holds one pty per pane. Enough of them
  and the host's pty pool runs dry — roughly 50 leaked servers once held 507 of
  511 ptys and every `tmux attach` on the machine failed with
  `fork failed: Device not configured`.
- **leaked eval binaries.** A `tests/eval` invocation that starts the TUI and
  never exits leaves an `agent-deck` process running out of its
  `agent-deck-eval-bin-*` temp dir, polling forever. Four of those were found
  running for eleven days in one sweep (#1747).

It is **not** part of the product, it is **not** installed by
`install.sh`, and it is **off by default**. Nothing in agent-deck needs it; it
exists for hosts that accumulate this debt.

## Why it is opt-in, and gated

The reaper's earlier version identified tmux servers by process name
(`pgrep -fx "tmux -C"`). On macOS a process keeps the argv it was exec'd with,
so the *default-socket server* auto-started by a bare `tmux -C` client was
itself named exactly `tmux -C` — the main server wearing a client's name. The
hourly job matched it and killed it. Every live session on the host died at
once, twice in one day.

The rewrite (#1736) replaced name matching with **path** matching, which is the
only identity that cannot be spoofed by a process's own argv:

| Target | Identity used | Never used |
| --- | --- | --- |
| tmux server | socket path (`tmux -S <sock> list-sessions`, then `kill-server` through the same path) | pid, process name, argv, `pgrep`/`pkill` |
| leaked eval binary | executing image path (`/proc/<pid>/exe` on Linux, lsof's `txt` descriptor on darwin) | `ps -o comm`, argv, basename |

Two consequences worth knowing:

- The socket named `default` is filtered out by name, in the candidate list
  itself, so it cannot be reached even if it somehow appears under a swept dir.
- A leaked eval binary whose temp dir has already been deleted is **not**
  reaped. Without the on-disk path there is no path-based identity left, and
  falling back to a name match is exactly the mistake that killed the fleet.

## What it reaps

Scan roots: `$TMUX_TMPDIR`, `/tmp`, `$TMPDIR` (deduplicated, symlinks
resolved). Go's `os.MkdirTemp("")` uses `$TMPDIR`, which on darwin is a
per-user `/var/folders/...` path, so both places have to be swept.

| Family | Where | Minted by |
| --- | --- | --- |
| `ad1031-*` | `<root>/tmux-<uid>/` | issue-1031 launch-race tests |
| `ad-attach-*` | `<root>/tmux-<uid>/` | `tests/eval/session` attach/restart isolation |
| `ad-fork-*` | `<root>/tmux-<uid>/` | `tests/eval/session` fork-PI isolation |
| `ad-tmux-*/`, `ad-sock-*/`, `agent-deck-test-sock-*/` | `<root>/` | `testutil.IsolateTmuxSocket` / `ShortTmuxSocket` |
| `agent-deck-eval-bin-*/agent-deck` | `<root>/` | `tests/eval/harness` build dir |

Every name is a test-only convention. Eval-bin processes are additionally
required to be older than `AGENT_DECK_EVAL_BIN_MAX_AGE_SECONDS` (default 1 day)
and owned by the current uid.

## Knobs

| Variable | Default | Meaning |
| --- | --- | --- |
| `DRY_RUN` | `0` | `1` logs every kill it *would* make and kills nothing. Also mirrors the log to stderr. |
| `AGENT_DECK_REAPER_LOG` | `$HOME/.agent-deck/logs/tmux-reaper.log` | Log file. Falls back to `$TMPDIR` with a warning when `HOME` is unset. |
| `AGENT_DECK_EVAL_BIN_MAX_AGE_SECONDS` | `86400` | Minimum age before a leaked eval binary is killed. |
| `AGENT_DECK_PTY_WARN_THRESHOLD` | `400` | Log (and notify) when pty usage exceeds this. |
| `AGENT_DECK_REAPER_TMP_ROOTS` | unset | Colon-separated list that **replaces** the default scan roots. Used by the smoke test to confine the script to its own sandbox. |

`--list-candidates` prints the target set and exits without probing or killing:

```
socket /tmp/ad-tmux-9f2a/tmux-501/s
evalbin 41207 1039288 /var/folders/.../agent-deck-eval-bin-8123/agent-deck
```

## Re-enabling it safely

The gate from #1744: **the smoke test must be green before the job runs
unattended.**

1. **CI must be green** on the `reap-stale-tmux.sh DRY_RUN smoke` job
   (`.github/workflows/eval-smoke.yml`, backed by
   `tests/ci/reap-stale-tmux.test.sh`). It seeds a live tmux server per socket
   family inside one throwaway sandbox, plus a decoy server named `default` and
   a same-named-but-wrong-path process, and asserts that every family is found,
   that no default socket is ever a target, and that `DRY_RUN` kills nothing.

2. **Run the same test locally on the machine you are enabling it on** — the
   darwin identity path (lsof) is a different code path from Linux CI:

   ```bash
   bash tests/ci/reap-stale-tmux.test.sh
   ```

3. **Dry-run by hand** and read the output before any plist exists:

   ```bash
   DRY_RUN=1 bash scripts/reap-stale-tmux.sh --list-candidates
   DRY_RUN=1 bash scripts/reap-stale-tmux.sh
   tail -20 ~/.agent-deck/logs/tmux-reaper.log
   ```

   Confirm with your own eyes that your live agent-deck sockets are not in the
   list. `agent-deck list` should be unchanged afterwards.

4. **Install the plist** from `scripts/launchd/com.agent-deck.tmux-reaper.plist.example`,
   with `DRY_RUN=1` still set:

   ```bash
   sed "s#/Users/REPLACE_ME#$HOME#g" \
     scripts/launchd/com.agent-deck.tmux-reaper.plist.example \
     > ~/Library/LaunchAgents/com.agent-deck.tmux-reaper.plist
   launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.agent-deck.tmux-reaper.plist
   launchctl kickstart -k "gui/$(id -u)/com.agent-deck.tmux-reaper"
   tail -20 ~/.agent-deck/logs/tmux-reaper.log
   ```

5. **Observe for a week.** Only then remove the `DRY_RUN` entry from the plist
   and reload it. An hourly job that kills things is worth having only after a
   week of evidence that it names the right targets.

Disable at any time:

```bash
launchctl bootout "gui/$(id -u)/com.agent-deck.tmux-reaper"
launchctl disable "gui/$(id -u)/com.agent-deck.tmux-reaper"
```

If you previously installed a personally-labelled version of this job, boot out
that label too — two reapers on one host is one reaper too many.

## Changing the script

Any change to the rules is a change to something that can kill every session on
a developer's machine. The bar:

- Add the new family to `tests/ci/reap-stale-tmux.test.sh` **first**, seeded as
  a live server, and watch it fail.
- Never introduce a name/argv match, not even as a fallback. If path identity is
  unavailable, the correct behaviour is to skip and log — which is what the
  script does.
- Keep `DRY_RUN` covering every phase. A phase that ignores `DRY_RUN` is a phase
  nobody can review before it runs.
