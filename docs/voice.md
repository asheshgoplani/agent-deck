# Voice commands

```
agent-deck voice state [--json]
agent-deck voice do [--json] [--dry-run] [--from …] [--on <id>] [--pending <token>] [--answer-delay-ms <n>] "<sentence>"
agent-deck needs-me [--json] [--timeout 3s]
```

`agent-deck voice <sub>` is the group for the voice desk. An unknown or missing subcommand prints the usage and exits 2.

## voice state

Reads the voice daemon's state file (`$CLAUDE_VOICE_STATE_DIR`, else `~/.claude/voice`: `current.json`, else `mirror.json`) and prints one object:

```json
{"on":true,"phase":"listening","extra":"","phaseSince":"2026-11-17T23:00:00Z","muted":false,
 "target":{"id":"…","title":"…","host":"","profile":"default"},"source":"","from":"local","updatedAt":"2026-11-17T23:00:01Z"}
```

- `phase` is one of six words, defined in `internal/voicestate` (`PhaseListening`, `PhaseHearing`, `PhaseThinking`, `PhaseSpeaking`, `PhaseResting`, `PhaseOff`). The daemon's raw words map to them with `voicestate.MapPhase`: `transcribing` is `hearing`; `muted`, `paused` (call) and `meeting` are `resting` (the mic is not taking commands), and `extra` says which: `muted`, `call`, `meeting`, or `""` for a plain rest. `muted` is also its own boolean.
- `on` is false whenever `phase` is `off`; there is never `on:true` with `off`.
- `from` is `local` (current.json), `mirror` (mirror.json) or `none`. `target.host` is `""` for this machine, else the remote name.
- `source` (mac, glasses, phone) is empty until the daemon's state file carries it.
- No live voice (no file, `on: false`, dead daemon, stale file) prints `{"on":false,"phase":"off",…,"from":"none"}` and exits 0.

## needs-me

Sessions whose status is `waiting`, longest first, across every local profile and every configured remote (`[remotes.*]`), asked in parallel. Each host gets `--timeout` (default 3 s); an unreachable or slow host is an entry in `errors`, never a failure. Read-only.

```json
{"count":1,"sessions":[{"id":"…","title":"…","group":"…","profile":"default","host":"","status":"waiting",
  "substate":"interactive-menu","reason":"asks a question","since":"2026-11-17T22:58:00Z","waiting_seconds":123}],
 "errors":[{"host":"box1","error":"timeout"}]}
```

`reason` is one line from the substate (and its detail for limits). `since` and `waiting_seconds` come only from an explicit `waiting_since` in the host's `list --json`; no host reports it yet, so they are empty/0 for now (last activity is not used: it can be the creation time and would order sessions wrongly). Sessions without it sort after those with it, then by title.

**Profiles.** Local: every profile on this machine. A remote is asked for its configured profile only (`[remotes.<name>] profile`, default `default`); sessions in the remote's other profiles are not listed.

**Errors.** `errors` entries carry fixed words: `timeout`, `unreachable` (remote could not be asked), `bad-reply`, `failed` (local). A remote is `host: "<name>"`; this machine is `host: ""` plus `profile`. Raw ssh text is never shown. `--timeout` is 0 to 3 s. Plain text prints one line per session (`title [host]: reason, waiting 2m`); errors go to stderr.

The Go entry point is `needsme.Collect(ctx, needsme.Options{Sources: needsme.DefaultSources(self, profile)})` in `internal/needsme`.

## voice do

The voice desk: one heard sentence in, one deck action and the short line to speak out. See [voice-desk.md](voice-desk.md).

## voice home

```
agent-deck voice home --json   {"id","title","host","source":"config|default|none"[,"error":"not found|ambiguous"]}
```

The session voice talks to by default and "go home" returns to: `[voice] home` (a session id, a title, or an id
prefix of six or more), else the profile's conductor session (the one named like the profile when there are
several), else none. `voice state --json` carries the same object as `home`, so the voice daemon, the phone and the
Mac app read one setting. The daemon keeps its own "go home" words; this only says where home is.
