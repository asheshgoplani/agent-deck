# Drive the session's Browser panel

`agent-deck browser <op>` lets a session use its own tab set in the AgentDeck
Mac app Browser panel: open pages, list and close tabs, click, type, read the
page text and take a screenshot. It works the same for local sessions and for
sessions on a remote box. There is no new socket: the session writes a request
frame to its own events log, the owner's app (which already follows that log)
acts and writes the answer back to the same log, and the CLI waits for it.

```sh
agent-deck browser open https://example.com/dashboard --new-tab
agent-deck browser tabs
agent-deck browser read --outline
agent-deck browser click --ref e12
agent-deck browser type --selector 'input[name=q]' --text 'agent deck' --submit
agent-deck browser shot --full --out /tmp/page.jpg
agent-deck browser close --tab t2
```

## Command

```
agent-deck browser open <url|file> [--tab ID | --new-tab] [--browser-profile NAME]
agent-deck browser tabs
agent-deck browser close --tab ID
agent-deck browser click (--ref R | --selector S | --text T) [--tab ID]
agent-deck browser type  (--ref R | --selector S) --text T [--submit] [--tab ID]
agent-deck browser read  [--outline] [--tab ID]
agent-deck browser shot  [--full] [--out FILE] [--tab ID]
common: --session <id|title>, --json, --timeout <duration>
```

Browser requests need two opt-ins in `config.toml` on the host that runs
the command:

- `[browser] enabled = true` (every profile), or `[profiles.<name>.browser]
  enabled = true` for one profile (a profile setting overrides the global one
  either way). Off by default. While off, every op and every `events publish
  --kind macapp.browser.request` (local, or one claiming to come from a
  remote) refuses before anything is published: exit 2, JSON code
  `BROWSER_DISABLED`, and a message naming the setting and the command:
  `agent-deck config set browser.enabled true` or
  `agent-deck config set profiles.<name>.browser.enabled true`.
- `[macapp] plugins = true`. While off, every op exits 1 with `browser
  requests are off: set [macapp] plugins = true`, and nothing is written.

Both are opt-ins, not security boundaries: an agent on that host can edit
its config, point `XDG_CONFIG_HOME` elsewhere or append to its own events log.
Plain `agent-deck open` needs no opt-in, so the check on `browser open` adds
no protection by itself. The policy that protects the owner lives in the Mac
app (see "Policy").

The op comes first. `--session` defaults to `$AGENTDECK_INSTANCE_ID`, then to
the agent-deck session that owns the calling tmux session; outside a session it
is required. IDs match exactly and duplicate titles are refused (the `open`
command's resolver). `--session` is routing, not permission.

`open` takes the same targets as `agent-deck open`: an absolute `http`/`https`
URL without credentials, or a file or directory (made absolute; a directory
selects `index.html`). File pages reach the app through `agent-deck file bundle`.

Default timeouts: 30 s for `open`, `tabs`, `close`, `read` and `shot`; 120 s for
`click` and `type`, so they can wait out the owner. `--timeout` overrides it.
The wait starts once the request is on disk.

| Exit | Meaning |
|------|---------|
| 0 | ok; for `open` with no answer in time: `Queued for AgentDeck: it opens when the app is connected` |
| 1 | error: refused arguments, `[macapp] plugins` off, unknown session, disabled bus, or an app error (`not_your_tab`, `bad_args`, `unknown_session`, `refused_url`) |
| 2 | `[browser] enabled` off for the profile (JSON code `BROWSER_DISABLED`); nothing was published |
| 3 | `not_connected`: no answer within the timeout (the app is offline or not following this log) |
| 4 | `owner_driving`: the owner kept using the page until the request expired |

Output: `open` prints `Opened <url> (tab <id>)`, `tabs` prints one line per tab
(`*` marks the active one) and the driver, `close` prints `Closed`, `click` and
`type` print the resulting URL, `read` prints the text (or outline), `shot`
prints the image path. `--json` prints the response object instead; for `shot`
its `result` carries `{mime, width, height, path}` in place of the image.

`shot` writes the decoded JPEG to `--out`, or to
`<data dir>/runtime/browser-shots/<session id>/<req_id>.jpg` (the data dir is
`~/.local/share/agent-deck`, or `~/.agent-deck` on legacy installs), with mode
0600 in a 0700 directory.

The browser command never invokes the `agentdeck://` URL scheme and never
launches anything; the app reads the log.

## Frames

All frames use the standard envelope `{cursor, event_id, ts, kind, session_id,
data}` with `session_id` = the calling session. These kinds are built in. The
app's replies (`macapp.browser.response`, `macapp.app.focus`) publish without
`[macapp] plugins = true`; `macapp.browser.request` needs it, and
`macapp.session.closed` is written by the host itself. The local plugin kinds
`macapp.browser.open` and `macapp.browser.reload` are unrelated and stay gated.

| kind | writer | data |
|------|--------|------|
| `macapp.browser.request` | session CLI | `{v:1, req_id, op, args, host, requested_at, expires_at}` |
| `macapp.browser.response` | app | `{v:1, req_id, request_event_id, ok, error?:{code, message}, result, driver, app_id}` |
| `macapp.app.focus` | app | `{app_id, mac, user, claimed_at, lease_until}` |
| `macapp.session.closed` | session core | `{reason:"removed"}`, or `{reason:"moved"}` on the source profile of a profile migration |

Times are UTC RFC 3339 (fractional seconds allowed). `req_id` is 32 hex
characters, unique per request. `host` is the source OS hostname; the app
derives the remote name from the follow that delivered the frame, never from
`host`. `expires_at` is `requested_at` plus the CLI timeout for interactive ops
and plus 24 hours for `open`, so a queued open still lands when the app
connects while a stale click never fires.

Request example:

```json
{"kind":"macapp.browser.request","session_id":"box-session-id","event_id":"9f2c...","data":{"v":1,"req_id":"5e0b...","op":"read","args":{"mode":"text"},"host":"agentbox","requested_at":"2026-10-13T10:00:00.123Z","expires_at":"2026-10-13T10:00:30.123Z"}}
```

The app answers on the same host and profile:

```sh
agent-deck events publish --kind macapp.browser.response --session box-session-id \
  --data '{"v":1,"req_id":"5e0b...","request_event_id":"9f2c...","ok":true,"result":{"url":"https://example.com/","title":"Example","text":"..."},"driver":"agent","app_id":"mac-1"}'
```

From a controller, the reply goes back with `agent-deck remote <name> events
publish --kind macapp.browser.response --session box-session-id --data-file -`
(payload on stdin). The box accepts it only for one of its own sessions; see
docs/events.md, "Remote reply publish".

The CLI subscribes before it publishes, accepts only a response whose
`session_id`, `req_id` and `request_event_id` all match, takes the first one
and ignores later ones.

## Ops

| op | args | result |
|----|------|--------|
| `open` | `{url \| path, tab_id?, new_tab?, browser_profile?}` | `{tab_id, url, title}` |
| `tabs` | `{}` | `{tabs:[{tab_id, url, title, active}], driver}` |
| `close` | `{tab_id}` | `{closed:true}` |
| `click` | `{tab_id?, ref \| selector \| text}` | `{url, title}` |
| `type` | `{tab_id?, ref \| selector, text, submit?}` | `{url}` |
| `read` | `{tab_id?, mode:"text" \| "outline"}` | `{url, title, text, refs?}` |
| `shot` | `{tab_id?, full_page?}` | `{mime:"image/jpeg", image_b64, width, height}` |

Each op accepts only its own fields. `open` needs exactly one of `url` or
`path` (`path` absolute, without `..`) and not both `tab_id` and `new_tab`;
`click` needs exactly one target; `type` needs one of `ref` or `selector` and
a non-empty `text`. Caps: `type` text 8 KiB, `read` text 256 KiB, `shot` image
1,400,000 bytes decoded (so a shot reply fits the 2 MiB remote reply cap with
its envelope). The CLI refuses oversized results. `browser_profile` names a Browser
panel profile (letters, digits, `.`, `_`, `-`, at most 64, no leading `.`;
not the agent-deck profile).

`req_id` is 32 lowercase hex characters; a request with any other `req_id` is
invalid. A response must carry `v: 1`; the CLI refuses any other version.
Everything the CLI prints from a response (page text, titles, URLs, tab ids,
app messages) has its control characters removed (page text keeps line
breaks and tabs), so a page cannot send escape sequences to the agent's
terminal.

### Results out of the log

The events log is shared by every reader of the profile and retained for a
while, so large results do not stay in it. `events publish --kind
macapp.browser.response` (which the app runs on the host that asked, locally
or through `remote <name> events publish`) moves a result over 16 KiB (page
text, screenshots) to a side file `<data dir>/runtime/browser-results/<profile>/<req_id>.json`,
mode 0600 in a 0700 directory, and the frame carries
`result_ref: {file, bytes, sha256}` instead of `result`. The waiting `agent-deck
browser` reads the file, checks its size and digest, prints the result and
deletes the file. Side files older than 10 minutes are swept on every such
publish and every `agent-deck browser` command. `type --text` (at most 8 KiB)
stays in the request frame, since the app reads requests from the log.

## Policy

The core cannot enforce a policy on what a session's browser may do: an agent
on a host controls that host's CLI, config and log. The Mac app is the
enforcement point, and the contract (`internal/macapp`, policy.go) is:

- **Addresses.** A request from a remote host (a session the app reaches over
  SSH) never loads, as the app resolves the name, for the first load,
  redirects and every subresource:
  - loopback, private, link-local (incl. cloud metadata `169.254.169.254`),
    unspecified / "this network" (`0.0.0.0/8`), multicast, broadcast
    (`255.255.255.255`), reserved (`240.0.0.0/4`), benchmarking
    (`198.18.0.0/15`) or IETF protocol (`192.0.0.0/24`) addresses;
  - **CGNAT `100.64.0.0/10`, which tailnets (Tailscale and the like) use**:
    a box page must not reach the owner's other machines over the tailnet;
  - IPv4-mapped, IPv4-compatible and NAT64 (`64:ff9b::/96`,
    `64:ff9b:1::/48`) IPv6 forms, and any address with a zone ID;
  - `localhost`, `*.localhost`, `*.local`, `*.lan`, `*.internal`,
    `home.arpa`, `*.ts.net` (tailnet MagicDNS) and single-label names.
- **Profiles.** A Browser panel profile is isolated per session: a session's
  pages never use the owner's own browsing profile, cookies or logins unless
  the owner turns that on for it in the app. A remote host never picks or
  switches a panel profile (`browser_profile` from a remote is refused).

As defence in depth, the CLI on a host the app reaches as a remote (not macOS,
or inside an SSH session) refuses such addresses in `browser open` and `open`
before it writes a request (`macapp.CheckRemoteURL`). It maps the host the
way a browser does first (UTS-46: full-width forms such as `１２７.０.０.１`
or `ｌｏｃａｌｈｏｓｔ`, the ideographic full stop in `127。0。0。1`,
percent-escapes) and refuses a host that does not map; it also refuses
numeric host forms like `2130706433`. It does no DNS, so it never replaces
the app's check. A local Mac session may still open its own dev server.

`read` returns text and element refs only and masks password input values.
There is no cookie, storage or script op, and `type` into a password field is
refused. A shot shows whatever the page shows; it goes only to the calling
session.

## Errors

| code | when | CLI exit |
|------|------|----------|
| `not_connected` | no answer in time (also usable by a relay) | 3 |
| `owner_driving` | a mutating op expired while the owner was using the page | 4 |
| `not_your_tab` | `tab_id` belongs to another session | 1 |
| `bad_args` | arguments the app cannot use | 1 |
| `unknown_session` | the app has no context for this session | 1 |
| `refused_url` | the app refused the URL | 1 |

## Routing and the driver

The app keys each session's tabs by `<remote>:<box session id>` (plain session
id for local sessions), where `<remote>` is the name of the follow that
delivered the frame. Ops act only on that key's tabs. Frames land only in the
session owner's Unix account log, so only that owner's apps see them. The app
acts on a request only when its `session_id` is a session the delivering host
reported, and never addresses another host's session through that follow (the
checks are listed in docs/events.md, "Mac app checks").

Driver state lives in the app per key. Owner input in the page sets the driver
to `owner`. Mutating ops (`open`, `close`, `click`, `type`) wait for 5 s of
owner idle or Hand back, then run if not expired, else answer `owner_driving`.
`read`, `tabs` and `shot` never wait and report `driver`.

## Focus claim

Several apps of one user may follow the same log; the last focused one acts.
An app publishes `macapp.app.focus` to each box it follows on focus and on
connect, with a 10 minute lease renewed every 5 minutes while connected. A
claim is valid while `app_id` is set and `claimed_at < now < lease_until`.
The holder is the valid claim with the newest `claimed_at`; on a tie the later
frame wins. Only the holder answers; the others stay silent and keep their
cursor. `internal/macapp` implements this as `FocusClaim.Valid` and
`ActiveClaim`.

## Offline queue

The durable log is the queue (32 x 8 MiB retained). An offline app misses
nothing: on connect it claims focus, replays from its saved cursor and drops
requests past `expires_at`. `open` therefore reports `Queued` and exits 0 when
nobody answers in time; every other op exits 3 and its request expires at the
CLI timeout.

When a session is removed (by any path: CLI, TUI, web, worktree finish,
conductor teardown) or moved to another profile, its core publishes
`macapp.session.closed` so the app can forget that key.
