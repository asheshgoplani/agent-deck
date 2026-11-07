# Switch to a session

`agent-deck switch <query>` shows a session in the AgentDeck Mac app, from any
terminal, host or tool (voice skill: `skills/agent-deck/references/switch.md`).

```bash
agent-deck switch "api server"            # title, part of one, or group words
agent-deck switch d00d                    # id or unique id prefix
agent-deck switch "voice 0956" --json     # scripts and voice: one JSON object
agent-deck switch docs --no-attach --wait 5s
```

## Matching

In order: exact id, unique id prefix (at least 4 characters), exact title
(case-insensitive), then a fuzzy score ported from the voice daemon's strict
`matchSession`: token overlap and edit distance over the title, with the group
path as extra words; threshold 0.55; a running or waiting session gets +0.02;
the best must lead the next by 0.15. A number in the query must appear exactly
("0956" never matches "0955"). Archived sessions are left out. It never
guesses: close scores, duplicate titles or a shared id prefix list up to five
choices (exit 3). No match exits 2.

## Delivery

1. On the Mac itself (macOS, not over SSH): `/usr/bin/open agentdeck://focus?session=<id>`.
2. Elsewhere, or when that fails: a `macapp.switch` frame on the profile's
   events bus, `session_id` = the session, data
   `{request_id, requested_at, host}`; the CLI waits `--wait` (default 2s,
   at most 2m) for `macapp.switch.ack` `{request_id, focused, error?}`. A box
   reaches the app over its events follow; the app answers through
   `agent-deck remote <name> events publish --kind macapp.switch.ack`.
3. No app reached: in a terminal (no `--json`, no `--no-attach`) it attaches
   there (`switch-client` inside tmux). Otherwise it reports `queued`: the
   frame stays for an app that connects later.

Both kinds publish without `[macapp] plugins`. The app acks a frame older
than 30 s with `{focused:false, error:"expired"}` and does not move.

## JSON

```json
{"status":"switched","query":"api servr","session":{"id":"a1b2c3d4-1700000001","title":"api-server","group":"work/backend","path":"/src/api","status":"running"},"via":"app","elapsed_ms":41}
{"status":"queued","query":"docs","session":{"id":"f2f2f2f2-1700000006","title":"docs","group":"work/backend","path":"/src/docs","status":"stopped"},"via":"none","elapsed_ms":2013}
{"status":"ambiguous","query":"voice test","via":"none","candidates":[{"id":"c0ffee00-1700000003","title":"voice test 0956","group":"lab","status":"waiting","score":0.92},{"id":"a1b2ffff-1700000002","title":"Voice Test 0955","group":"lab","status":"idle","score":0.9}],"elapsed_ms":3}
{"status":"not_found","query":"kubernetes","via":"none","elapsed_ms":3}
```

`status` is `switched`, `queued`, `attached` (terminal only, never with
`--json`), `ambiguous` or `not_found`; `via` is `app`, `terminal` or `none`.
`session` is present only for one match, `candidates` only when ambiguous.
