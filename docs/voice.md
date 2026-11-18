# Voice commands

```
agent-deck voice state [--json]
agent-deck needs-me [--json] [--timeout 3s]
```

`agent-deck voice <sub>` is the group for the voice desk. An unknown or missing subcommand prints the usage and exits 2.

## voice state

Reads the voice daemon's state file (`$CLAUDE_VOICE_STATE_DIR`, else `~/.claude/voice`: `current.json`, else `mirror.json`) and prints one object:

```json
{"on":true,"phase":"listening","extra":"","phaseSince":"2026-11-17T23:00:00Z","muted":false,
 "target":{"id":"…","title":"…","host":"","profile":"default"},"source":"","from":"local","updatedAt":"2026-11-17T23:00:01Z"}
```

- `phase` is one of six words, defined in `internal/voicestate` (`PhaseListening`, `PhaseHearing`, `PhaseThinking`, `PhaseSpeaking`, `PhaseResting`, `PhaseOff`). The daemon's raw words map to them with `voicestate.MapPhase`: `transcribing` is `hearing`; `muted`, `paused` (call) and `meeting` are `resting` (the mic is not taking commands), and `extra` says which: `muted`, `call`, `meeting`, or `""` for a plain rest. `muted` is also its own boolean, and `extra` comes from the raw phase only: a call or meeting rest while also muted has `extra` `call` or `meeting` and `muted: true`; the plain line then says both (`voice: resting (call, muted)`).
- `on` is false whenever `phase` is `off`; there is never `on:true` with `off`.
- `from` is `local` (current.json), `mirror` (mirror.json) or `none`. `origin` is the daemon's machine as `voice.state` frames name it (a mirror's `from`); `""` for this machine's own file or no voice. `target.host` is `""` for this machine, else the remote name.
- `source` (mac, glasses, phone) is empty until the daemon's state file carries it.
- `switching` is `{id, title, host, profile}` while a spoken switch is under way (the daemon writes it before its first ssh call), else `null`. It is a hint only: `target` is the session that holds the mic.
- No live voice (no file, `on: false`, dead daemon, stale file) prints `{"on":false,"phase":"off",…,"from":"none"}` and exits 0.

## needs-me

Sessions whose status is `waiting`, longest first, across every local profile and every configured remote (`[remotes.*]`), asked in parallel. Each host gets `--timeout` (default 3 s); an unreachable or slow host is an entry in `errors`, never a failure. Read-only.

```json
{"count":1,"sessions":[{"id":"…","title":"…","group":"…","profile":"default","host":"","status":"waiting",
  "substate":"interactive-menu","reason":"asks a question","since":"2026-11-17T22:58:00Z","waiting_seconds":123}],
 "errors":[{"host":"box1","error":"timeout"}]}
```

`reason` is one line from the substate (and its detail for limits). `since` and `waiting_seconds` come from `waiting_since` in each host's `list --json`: the time the session's status changed to `waiting`, recorded in that profile's `state.db` (table `status_changes`, kept by triggers on every status write, so it holds the time of whichever agent-deck process wrote the change, such as the TUI or the notify daemon). Last activity is not used: it can be the creation time and would order sessions wrongly. A session with no recorded change to waiting (one that has waited since before an upgrade to this version), or with a `waiting_since` in the future (a skewed remote clock), has `since: ""` and `waiting_seconds: 0` and sorts after the others; ties go by title, then id. A remote that runs an older agent-deck sends no `waiting_since`, so its sessions sort last.

**Profiles.** Local: every profile on this machine. A remote is asked for its configured profile only (`[remotes.<name>] profile`, default `default`); sessions in the remote's other profiles are not listed.

**Errors.** `errors` entries carry fixed words: `timeout`; `unreachable` (a remote that could not be asked: ssh failed, or its agent-deck is missing or exited non-zero); `bad-reply` (the reply was not a session list); `failed` (a local profile that could not be listed, or a source that crashed, local or remote). A remote is `host: "<name>"`; this machine is `host: ""` plus `profile`. Raw ssh text is never shown. `--timeout` is more than 0 and at most 3 s. Plain text prints one line per session (`title [host]: reason, waiting 2m`); errors go to stderr.

The Go entry point is `needsme.Collect(ctx, needsme.Options{Sources: needsme.DefaultSources(self, profile)})` in `internal/needsme`.

## voice relay (live state on every host)

```
agent-deck voice relay --to <remote>[,<remote>...] --self <name>
```

Runs on the machine of the voice daemon. Every change of the voice state (phase, mic target, mute and the other
flags) goes on this host's event bus as a `voice.state` frame and, over one ssh link per remote, to `agent-deck
voice mirror-in` on each named remote, which writes that host's `mirror.json` and publishes the same frame there.
`--self` is this machine's name in those remotes' own `[remotes.<name>]` config (a target on this machine is shown
under it there). Heard and spoken text never leaves this machine. A dead daemon reaches every host as `off`.

A follower that starts reads the snapshot first: `agent-deck voice state --json` here, `agent-deck remote <name> voice
state --json` for a remote (read-only, forwarded over ssh), or the `hello` event of `GET /events/state`.

Followers: `agent-deck events follow --kind voice.`, `agent-deck remote <name> events follow --jsonl --kind voice.`,
and `GET /events/state` on `agent-deck web` (server-sent events with resume by cursor). Frame fields, the `host`
rule and the web route: [events.md](events.md).
