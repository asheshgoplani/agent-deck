Hi @marekaf — thanks for the detailed report. I've spent some time on this and need a few more data points before I can land a fix, because the bisection so far is pointing away from the v1.7.69→v1.7.70 diff itself.

## What I verified

**1. The Claude spawn path is byte-identical between v1.7.69 and v1.7.70.**

```
git diff v1.7.69 v1.7.70 -- internal/session/instance.go internal/tmux/tmux.go
```

…shows the only `instance.go` change is to `buildCodexCommand` (the #756 stale-rollout gate). The Codex change cannot reach a Claude command path. The `tmux.go` change is a bug fix to `Session.Exists()` for isolated sockets — it doesn't touch session creation.

**2. `bash -lc` is not new in v1.7.70.**

`wrapRespawnCommand` → `buildBashLCCommand` has been emitting `<bash> -lc <quoted>` since commit `c63a08b` (`fix(sandbox): Harden probe exec and respawn bash wrapping`), which has shipped in every release since v1.5.1. The previous `bash -ic` form is gone for everyone, not just v1.7.70 users.

**3. `--permission-mode auto` is not hardcoded.**

The only emission site is `buildClaudeExtraFlags` (`internal/session/instance.go`), gated on `opts.AutoMode`. When `ToolOptionsJSON` is empty, the fallback is `NewClaudeOptions(userConfig)` which sets `AutoMode = config.Claude.AutoMode`. With `[claude] auto_mode = false`, the flag should not appear.

That makes the persisted blob the most likely source of the unexpected `auto`. I'd like to confirm.

**4. Locally I cannot reproduce the failure.**

On Linux + claude 2.1.121 + tmux 3.5+, `claude --session-id <new-uuid> --permission-mode auto` under a real PTY shows the first-use *"Enable auto mode? Yes / Yes / No, exit"* consent dialog and **stays waiting**. Under agent-deck, the corresponding session reports `status=waiting` correctly — no respawn loop, no `control_pipe_connect_failed`. So fresh-session `--session-id + --permission-mode auto` is not categorically broken.

## Where I'm stuck

Your symptom — pane dies immediately, hash regenerates each retry, `respawn-pane` says `can't find session` — means the inner `claude` is exiting (or being killed) before agent-deck's control pipe can attach. On my repro it doesn't exit. The remaining variables that differ from my setup are macOS-specific (Apple bash 5.1.16 via Homebrew, iTerm2 3.6.10, tmux 3.1c).

**tmux 3.1c is over five years old.** v1.7.70 added a startup warning for `tmux < 3.6b` because of an unfixed control-mode NULL-deref (tmux #4980) that can crash the entire server. It's plausible — but unproven — that 3.1c has its own race in the same area, and that's what's nuking your sessions during the control-pipe handshake. The `: 0` text in `connect pipe for ...: session ...: 0` looks like a malformed `%error` from tmux (the protocol field that should carry the error message contains just `0`), which is consistent with control-mode flakiness in old tmux.

## What would unblock me

Please run these and paste the output. None of it leaks secrets beyond your own config file.

**(a) The persisted options on a broken session.** Pick one of your `ai-*` sessions that you know was created *without* toggling auto mode in the dialog:

```
agent-deck session show --json <session-name> | jq '{id, tool, command, claude_session_id, tool_options}'
```

The `tool_options` field is the answer to "is auto really persisted on this instance, or is it falling through to config defaults". Either result is informative.

**(b) Your `[claude]` block, verbatim:**

```
sed -n '/^\[claude\]/,/^\[/p' ~/.agent-deck/config.toml | head -20
```

I want to see whether `auto_mode` is truly `false` and whether `dangerous_mode` is set (a stray `dangerous_mode = false` plus `auto_mode = true` upstream would also explain the flag).

**(c) The very first lines from the debug log, *before* the respawn loop starts.** Specifically:
- The `start_command` / `tmux_new_session` line that shows the *initial* spawn (initial start uses `bash -c`, not `bash -lc` — I want to confirm that's also the form you saw on first start).
- Any `claude_session_id_detected` or `hook_session_anchor_set` lines (so I can tell whether the first claude process at least lived long enough to emit its session-start hook).
- The first `pipe_connected` *or* `pipe_died_during_handshake` line.

If you can capture this with `AGENTDECK_LOG_LEVEL=debug agent-deck` redirected to a file from a clean start, that would be ideal.

**(d) A direct claude smoke test, outside agent-deck, in iTerm2:**

```
cd /tmp && /opt/homebrew/bin/bash -lc 'claude --session-id 11111111-2222-3333-4444-555555555555 --permission-mode auto'
```

Two questions: (1) does the `Enable auto mode?` prompt actually render, and (2) does claude stay running for at least 30 seconds without input? If claude exits on its own under that exact bash invocation, the bug is in claude / your bash profile, not agent-deck. If it stays running, the bug is in agent-deck's tmux-3.1c code path and we have a much narrower target.

**(e) A bash-profile sanity check:**

```
/opt/homebrew/bin/bash -lc 'echo OK' ; echo "exit=$?"
```

Just to rule out a `set -e` / `exit` / `unset` failure in your `~/.bash_profile` chain that would kill the spawn before claude runs.

## Workaround that should still work

The `agent-deck add <path>` flow you mentioned (let claude write its own JSONL first, then import) is fine because that path uses `claude --resume <existing-uuid>` — which doesn't trigger the auto-mode consent dialog and doesn't depend on first-spawn timing. Until we narrow this down, sticking with that is the right call.

Once I have the data above I should be able to either land a fix or escalate to a specific tmux/claude version dependency check. Sorry for the friction.
