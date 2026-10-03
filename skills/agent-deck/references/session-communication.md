# Session-to-Session Communication

How sessions message each other, collect child results, and what each channel guarantees.

**Use when:** a session needs to message another session, a parent needs to collect child
results, anyone asks "how do I notify the conductor", "did my child finish", "how do I read
another session's answer", or a send seems to have vanished. Every command below was
verified against the installed binary (v1.16.11-rc.2). From any non-interactive shell
(cron, systemd, hooks) always pass `-p <profile>` explicitly, or session resolution
silently uses the default profile and the target is "not found".

## The channel map

| Channel | Direction | Command | Guarantee |
|---|---|---|---|
| **send** | any → any live session | `session send <id> "msg"` | Best-effort keystrokes into the pane (or, opt-in, Claude's own messaging socket). `--json` carries a stable 3-way `confirmation` field (`confirmed`/`unknown`/`failed`) — read that, not the 11-value `delivery` diagnostic. Since 1.16.17 a plain `--json` send (no `--wait`/`--stream`/`--no-wait`/`--draft`/`--defer-if-busy`) returns at once with `send_id`, `verdict:"queued"`, `delivery:"queued"` and `confirmation:"unknown"`; a background worker types it (into a busy Claude immediately, other harnesses when idle) and `session send-status <send_id> --json` reports the verdict `delivered`/`queued`/`unknown` as evidence arrives (`submitted`, `queued`, `delivered`, `unverified`, `queued_socket`, `line_too_long`, `menu_open`, `pane_gone`, `typed_not_submitted`, `no_evidence`, `send_failed`, `composer_blocked`, `socket_write_failed`). NOT durable: if the send fails or the sender dies, the message is gone. |
| **output** | read a session's last reply | `session output <id> -q` | Read-only transcript snapshot; non-consuming; `--pane` returns the tmux pane capture instead. Default text is ANSI-stripped and capped at `--max-tokens` (default 25000) with the full output kept on disk; `--json`/`-q`/`--copy` carry the full source. |
| **children** | parent reads its child fleet | `session children --json`, `--follow [--until-done]` | Read-only; merges live status with the completion ledger; explicitly does NOT clear the inbox. |
| **inbox drain** | child completions → parent | `inbox drain self --json` | THE durable channel (records are tiered `urgent`/`info` and carry the child's new `text`, so act on the record instead of re-reading the child; only `urgent` wakes an idle parent, see `[inbox]` in config-reference): fsync'd append + WAL, at-least-once delivery with exactly-once effects (turn-fingerprint dedup), survives crashes and restarts. Last-wins PER CHILD: intermediate events are dropped by design. Single-profile only. Draining consumes. |
| **transition events** | daemon → parent's inbox | automatic (requires `parent_session_id`) | Only `running → waiting/error/idle` edges fire; deduped (90s + 2h windows); sessions with no parent link are WARN-logged once and DROPPED. |
| **[DONE] sentinel** | worker asserts completion | worker prints `===AGENTDECK_DONE=== status=ok summary=...` | Idempotent per distinct completion; the only trustworthy "finished" signal (see [Completion sentinel](sub-agents.md#completion-sentinel-trustworthy-worker-finished-signal-1186)). |
| **heartbeat** | bridge → conductor | bridge-driven `send --wait -q` | Lossy by design: skipped while the conductor is busy, never queued; only fires when waiting>0 or error>0. |
| **handoff** | Claude → Codex context copy | `session handoff <id> [--json --out file]` | One-shot read-only transcript copy (32k-char tail budget); no ongoing link afterwards. |

## Verified commands

```bash
agent-deck -p <profile> session send <id> "single line message"          # default: waits for readiness, verifies
agent-deck -p <profile> session send <id> "answer now?" --wait -q --timeout 300s   # send + wait + raw reply, one call
agent-deck -p <profile> session send <id> "nudge" --no-wait -q           # fire immediately (heartbeats/nudges)
agent-deck -p <profile> session send <id> "done ping" --defer-if-busy --defer-timeout 30m  # hold until idle; DROPS at timeout
git diff | agent-deck -p <profile> session send <id> --message-file -    # long/multiline payload safely from stdin
agent-deck -p <profile> session send <id> "draft text" --draft           # type without submitting
agent-deck -p <profile> session output <id> -q                           # read last response (raw text)
agent-deck -p <profile> session output <id> --pane                       # pane capture, ANSI stripped + capped (fallback when transcript read refuses)
agent-deck -p <profile> session children --json                          # child fleet snapshot + parent id
agent-deck -p <profile> session children --follow --until-done           # JSONL event stream, exits when all children terminal
agent-deck -p <profile> inbox drain self --json                          # heartbeat fallback; your UserPromptSubmit hook already drains into [agent-deck inbox] context at every turn start
agent-deck -p <profile> inbox peek self                                  # look without consuming
agent-deck -p <profile> session show <id> --json                         # has parent_session_id + substate (list --json does NOT)
agent-deck -p <profile> session handoff <id> --json                      # build cross-tool handoff prompt, read-only
agent-deck -p <profile> session search "term" --json --limit 5           # substring search across Claude transcripts
```

## What each channel guarantees, and what it does not

- **`send` result is evidence-graded, not binary.** With `--json --wait` (or `--no-wait`) read the `delivery`
  field: `submitted` / `unverified` / `typed_not_submitted` / `no_evidence` / `send_failed`.
  A plain `--json` send is queued and answers `delivery:"queued"`; poll `session send-status <send_id> --json`.
  Exit 0 plus `delivery:"unverified"` is NOT proof the turn started (#1793: a boundary-sized
  Codex send can pass every check and never submit). Confirm with `session show --json`
  (status flips to running) or `output` when the answer matters.
- **A non-zero send exit does NOT prove non-delivery.** The 3-second tmux send-keys
  deadline can kill a send that already landed in the pane. Blind retry on non-zero =
  double-send. Check the pane (`session output <id> --pane`) before resending.
- **Only the inbox is durable.** `send` in every mode dies with the sender. Child→parent
  completions ride the inbox; everything else (parent→child, peer→peer, cross-profile)
  is fire-and-forget keystrokes. Cross-profile parents are a terminal drop.
- **Inbox is last-wins per child.** A parent draining after three child status changes
  sees only the latest. There is no replayable message history between sessions; use
  files (RESULTS.md, task-log.md) for anything that must not collapse.
- **`--defer-if-busy` drops on timeout** with a non-zero exit; the message is NOT queued
  for later. Treat a defer timeout as undelivered and decide explicitly.
- **Heartbeats are lossy on purpose** (skip while busy, never queued); the durable
  completions arrive via the inbox drain that every heartbeat starts with.

## Pitfalls and workarounds

- **Single-line only for positional messages.** Embedded newlines make `send` exit 1,
  and a backgrounded wrapper swallows the failure (silent loss). For multiline payloads
  use `--message-file <file>` or `--message-file -` (stdin) — that path is safe.
- **#876 Enter fallback after `--no-wait`.** On a freshly launched session `--no-wait`
  can leave the message typed but not submitted. Wait ~3s, then send an idempotent
  Enter: `tmux send-keys -t "$(agent-deck -p <profile> session show --json <id> | jq -r .tmux_session)" Enter`.
  Note `--no-wait` deliberately disables auto-resend (double-send protection), so this
  manual fallback is on you.
- **Never spam a busy session.** A `running` target queues your keystrokes into its
  composer mid-turn. Use `--defer-if-busy`, or poll `session show --json` until
  `status` leaves `running`. Bound every repeated signal: 3 identical sends/nudges
  with no state change means stop and change tactic, not send a fourth.
- **Read `substate`, not just `status`.** `error` + substate `auth-401` means dead
  credentials (restarting will NOT fix it; the fleet HOLDs these); substate
  `model-unavailable` means the model is down. Never restart-loop either. `substate`
  is omitempty: absent means none, not healthy-confirmed.
- **Topology reads:** `list --json` omits `parent_session_id`. To see linkage use
  `session show --json <id>` or `session children`. Verify every `launch` created the
  linkage you expect; children born without a parent link get NO transition events.
- **Addressing:** prefer id prefixes over titles (a Claude session can rename itself and
  break title targeting; `session set-title-lock <id> on` prevents that). Always `-p`
  from non-interactive senders.
- **If `session output` refuses with "colliding transcript"** (one claude_session_id
  claimed by two live instances, seen after restart/fork), fall back to
  `session output <id> --pane` — read-only and always available.
- **Slash commands via send:** wrap conversationally ("Please run /cmd ...") — bare
  `/cmd` sends to a freshly restarted child are ignored.
