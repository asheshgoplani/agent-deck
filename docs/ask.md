# Ask the owner (`agent-deck ask`)

An agent asks the owner for a secret, a choice or one short line of text.
The owner answers in a dialog: the AgentDeck Mac app, else the TUI modal,
else a command he runs in his own terminal. A secret goes from the dialog to
this host's vault and never reaches a model context: not the asking agent's
process or environment, its tmux pane, transcript, hooks, the events log,
inbox, notifications, logs, argv or crash dumps.

```sh
agent-deck ask secret conductor/GH_TOKEN_DECKBOX --reason "GitHub key with workflow permission"
# stdout, once the owner saved it:
stored conductor/GH_TOKEN_DECKBOX (replaced=no, by app)

agent-deck ask choice "Which database?" --option staging --option prod
prod

agent-deck ask text "Which branch should I rebase on?"
main
```

Options for all three: `--session <id|title>` (default `$AGENTDECK_INSTANCE_ID`,
then the agent-deck session of the calling tmux session; the same resolution as
`open`), `--reason "why"` (shown as "The agent says: ..."), `--wait 10m` (the
request expires then; at most `30m`), `--fallback-after 20s`, `--no-fallback`,
`--json`. `ask secret` also takes `--replace` (see below).

| Exit | Meaning |
|------|---------|
| 0 | stored (secret) or answered (choice, text) |
| 2 | refused: bad name, scope not allowed, rate limit, one request already open, name exists without `--replace`, ... |
| 3 | cancelled (`cancelled by app` on stderr; Ctrl-C/SIGTERM cancels as `asker`) |
| 4 | timed out (`timed out after 10m0s` on stderr) |
| 5 | nothing can show it (`--no-fallback`) or no vault on this host |
| 1 | other error |

stdout carries only the result line: `stored scope/NAME (replaced=yes|no, by
app|tui|terminal)`, or `stored scope/NAME (direct deck-secret set,
unverified)` when the owner used the direct route; for choice and text the
answer and nothing else. With `--json`:
`{"status":"stored|answered|cancelled|expired","name":...,"replaced":...,"answered_by":...,"answer":...}`
(`answer` only for choice and text).

## Rules

- **Names** are `scope/NAME`: scope `[a-z0-9_-]`, NAME `[A-Za-z0-9_.-]` up to 64
  characters, no leading `.`, no `..`. The scope must be in `[ask] secret_scopes`
  (default `["conductor/"]`).
- **Existing names**: `ask secret` asks the vault (`deck-secret exists`) first.
  A name that exists is refused unless `--replace` is given; then every dialog
  shows a red "REPLACES an existing secret" row and the asker warns on stderr.
  `--replace` on a name that does not exist is refused too. Deliver checks
  again under the lock and refuses when the answer changed.
- **One open request per session**; a request whose asker died no longer counts.
- **Rate**: 5 requests per session and 20 per profile in any hour.
- **Expiry**: `--wait` (default 10m, at most 30m). An expired request can never
  be delivered, even when the asker is gone.
- **Caller**: `$AGENTDECK_INSTANCE_ID`, when set, must be the resolved session.
  On Linux the asker walks its `/proc` parent chain to the pane of a known
  session; inside a session it can only ask as that session. A caller outside
  every agent-deck pane is shown as "Unverified caller".
- **Sandboxed sessions** may ask only with `[ask] allow_sandboxed = true`.
- **Untrusted text**: reason (200), question (300) and options (80, at most 6)
  are shown as plain text: control, bidi and zero-width characters are
  stripped, newlines fold to spaces. Options must already be plain one-line
  text.
- **Text and choice answers** that look like credentials (`ghp_`,
  `github_pat_`, `sk-`, `AKIA`, `xoxb-`, PEM headers, a long high-entropy run)
  are refused: "looks like a secret; ask with `ask secret`". They are cleared
  from the record once the asker printed them.

## Flow and wire contract

Records live in `<profile dir>/ask/<request_id>.json` (dir 0700, files 0600,
read with `O_NOFOLLOW`, replaced by rename under a flock on `ask/.lock`). A
record never holds a secret value.

1. `ask secret|choice|text` validates, writes the record and commits
   `macapp.ask` on the profile bus (only this command publishes it, in process):

   ```json
   {"kind":"macapp.ask","session_id":"<session>","event_id":"<ask_event_id>","data":{"request_id":"3f9c0a1b2c3d4e5f","kind":"secret","name":"conductor/GH_TOKEN_DECKBOX","question":"","options":[],"reason":"GitHub key with workflow permission","replace":false,"session_title":"Conductor","host":"deckbox","profile":"ashesh","caller_verified":true,"requested_at":"2026-10-24T13:31:00.123Z","expires_at":"2026-10-24T13:41:00.123Z"}}
   ```

2. A UI that shows the request picks a random 16-hex nonce and announces it:
   the app with `agent-deck [-p P] events publish --kind macapp.ask.seen
   --session <session> --data-file -` (ungated; a remote reply kind, so
   `remote <box> events publish` forwards it), the TUI in process
   (`ask.MarkSeen`):
   `{"request_id":"...","ask_event_id":"...","nonce":"<16 hex>","by":"app|tui"}`.
   The asker records it in the request (`seen_by`) when the frame names the
   request, its `ask_event_id` and its session; the TUI and `ask answer`
   record theirs directly. Deliver accepts only a nonce recorded this way, so
   the app waits until `ask show <id> --json` lists it in `seen_by` before it
   enables Save (a request whose asker is gone cannot be answered from the
   app and simply expires).
3. The owner answers. The UI runs `agent-deck [-p P] ask deliver <request_id>`
   (the app over its ssh route) with stdin: line 1 the JSON header of what it
   displayed, then the value bytes to EOF:

   ```
   {"request_id":"3f9c0a1b2c3d4e5f","kind":"secret","name":"conductor/GH_TOKEN_DECKBOX","replace":false,"nonce":"<the UI's nonce>","answered_by":"app"}
   <value>
   ```

   One trailing newline is stripped; at most 64 KiB; empty is refused. A
   choice is exactly one of the options; a text answer is one line of at most
   500 bytes. Deliver refuses unless the request is pending and not expired,
   every header field equals the record and the nonce is one that UI
   announced; so a changed record cannot send the value to a name the owner
   did not see, and a replay finds the request no longer pending. Success:
   exit 0, stdout `{"status":"stored","replaced":"no"}` (or `"answered"`).
   Refusal: exit 2 and a fixed message on stderr (`expired`, `not pending`,
   `header does not match the request`, `vault refused (exit N)`, ...).
4. The host publishes `macapp.ask.ack` (only the host: `ask deliver`, `ask
   cancel`, the asker on expiry or interrupt; `events publish` refuses it):
   `{"request_id":"...","ask_event_id":"...","status":"stored|answered|cancelled|expired","answered_by":"app|tui|terminal|direct|asker","replaced":"yes|no"}`.
   No answer text. A dialog that sees an ack it did not cause shows "Answered
   elsewhere (<by>, <time>), not by you".
5. Cancel: `agent-deck [-p P] ask cancel <request_id> [--by app|tui|terminal]
   --nonce N` prints `{"status":"cancelled"}`; the asker exits 3. The nonce
   is required: it must be the one that UI announced, so nobody else can end
   a request a dialog shows. A request no dialog showed (the app's mute of a
   queued request) is cancelled with `--unshown` instead of `--nonce`, which
   is refused once any UI recorded a seen and is noted in the record
   (`cancelled_unshown`). Ctrl-C or SIGTERM of the asker cancels as `asker`;
   that is not a CLI option.
6. A seen hides the request from the TUI (an app seen) and from the asker's
   terminal fallback (any seen) only for 30 s: `macapp.ask.seen` arrives over
   the ungated `events publish`, so with no deliver or cancel within 30 s of
   the seen the TUI and the fallback show the request anyway, and a forged
   seen cannot make it time out silently.

UI-side reads never print a value: `ask list [--json] [--all]` (pending
requests; `--all` adds finished ones kept 24 h) and `ask show <id> [--json]`
(the record without its answer and without nonces; a pending request past its
deadline reads as expired). Finished records older than 24 h move to
`ask/trash/`.

## The vault

A secret is written by `/usr/local/bin/deck-secret set <scope/NAME>` with the
value on the child's stdin. The path is fixed: there is no config key or
environment variable for it. Before each run the file and every parent dir
must be owned by root and not writable by group or others, else deliver
refuses. The child gets a clean environment: `PATH=/usr/local/bin:/usr/bin:/bin`,
`HOME` and `AGENTDECK_SESSION_ID=<asking session>`, which deck-secret's audit
log records. Its output is read into a 4 KiB buffer, parsed only for
`replaced=yes|no`, and never printed. `ask deliver` and `ask answer` set
`PR_SET_DUMPABLE=0` and `RLIMIT_CORE=0` first; the value stays a byte slice
that is zeroed after the vault's stdin closed.

A host without the vault refuses `ask secret` with exit 5, "no vault
configured on this host" (Mac-only setups: a Keychain route is a follow-up).

## Fallbacks

When no UI marked the request seen within `--fallback-after` (default 20 s),
or the last seen is 30 s old with no deliver or cancel, the asker prints once
on stderr:

```
No AgentDeck app or TUI is showing this request. In your own terminal run:
  agent-deck -p ashesh ask answer 3f9c0a1b2c3d4e5f
(or: deck-secret set conductor/GH_TOKEN_DECKBOX)
```

and keeps waiting until the deadline (exit 4). `--no-fallback` exits 5
instead when nothing ever marked the request seen. `ask answer` works only with a terminal on stdin (an agent cannot
pipe into it): it prints the host's facts, prompts hidden (twice for a
secret) and calls the same deliver. The `deck-secret set` line appears only
when the name did not exist; the asker then polls `deck-secret exists` every
2 s and reports `stored ... (direct deck-secret set, unverified)`.

## Config

```toml
[ask]
enabled = true                 # false refuses every request and deliver
secret_scopes = ["conductor/"] # scopes ask secret may write; [] allows none
allow_sandboxed = false        # let sessions that run in a sandbox ask
```

With `AGENTDECK_DEBUG=1` every `ask` command logs the request lifecycle
(component `ask`: ids, kinds, names, states and who acted; never a value, an
answer or a nonce) to the XDG cache `debug.log`, like the TUI.

## Threat model

Agents run as the same unix user as the owner's tools. What `ask`
guarantees: the value never reaches a model context by accident (none of
the stores above, and a test greps all of them for a sentinel), every vault
write is audited with the asking session's id, and the owner always sees
which host, session and name a value goes to. What it cannot guarantee:
secrecy against a deliberately hostile agent with the same uid, which could
replace the agent-deck binary or rc files, or run `deck-secret get` (audited).
Real separation needs agents under their own unix user. `secret_scopes`,
`allow_sandboxed` and `enabled` live in config the agent can edit: they are
guard rails, not security boundaries, as for `open` (docs/macapp-open.md).

## In process (TUI)

`internal/ask` exports the same operations: `List`, `ListAll`, `Show`,
`MarkSeen`, `Deliver`, `Cancel` (see `internal/ask/doc.go`). The TUI calls
`Deliver` in process with the value as a `[]byte`, never through argv.

### The TUI modal (`internal/ui/ask_dialog.go`)

- The TUI polls its own profile's pending requests on its tick (at most once a
  second, off the UI goroutine) and opens a modal for the oldest one the app
  has not marked seen in the last 30 s, once the app has had 3 s to do so. One modal at a time;
  "N more waiting" shows the queue.
- It opens only on the dashboard: never while a session is attached or open
  in insert / embedded mode (keys would go to the agent's pane), never over
  another dialog, never while you are typing (1 s guard), never on a terminal
  below the minimum size. While it is open it takes every key and mouse event;
  no key reaches the keypress log, telemetry or a pane.
- On screen it calls `MarkSeen(profile, id, "tui")` and keeps the nonce for
  the deliver header, which copies the record (`request_id`, `kind`, `name`,
  `replace`) with `answered_by: "tui"`. Save is disabled until the nonce is
  back and the field is non-empty.
- Secret: masked input (`•`), no show toggle (the TUI may run in tmux, where
  an agent can `capture-pane`), no clipboard read (terminal paste works). It
  shows host, session, profile, name, a red "REPLACES an existing secret" row
  when `replace`, "Unverified caller (outside any agent-deck session)" when the
  caller is not verified, the reason as "The agent says:" (sanitized, at most 2
  lines) and the expiry countdown. Save and Cancel are the same width; Enter
  saves, Esc cancels. Choice: the options and Cancel. Text: a plain input and
  the fixed line "Your answer goes to the agent and its transcript. Never type
  a secret here."
- The input is reset when Save is pressed, before `Deliver` runs in a
  `tea.Cmd`; `Deliver` zeroes the bytes. A retryable refusal (vault refused,
  looks like a secret, ...) returns to an empty field with the fixed message.
- Cancel starts a 30 s cooldown for that session. A request answered,
  cancelled or expired elsewhere while open shows "Answered elsewhere (<by>),
  not by you" (or "Cancelled elsewhere" / "Expired") until a key closes it.
