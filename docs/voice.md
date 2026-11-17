# Voice commands

```
agent-deck voice state [--json]
agent-deck needs-me [--json] [--timeout 3s]
```

`agent-deck voice <sub>` is the group for the voice desk. An unknown or missing subcommand prints the usage and exits 2.

## voice state

Reads the voice daemon's state file (`$CLAUDE_VOICE_STATE_DIR`, else `~/.claude/voice`: `current.json`, else `mirror.json`) and prints one object:

```json
{"on":true,"phase":"listening","phaseSince":"2026-11-17T23:00:00Z","muted":false,
 "target":{"id":"…","title":"…","host":"","profile":"default"},"source":"","from":"local","updatedAt":"2026-11-17T23:00:01Z"}
```

- `phase` is one of six words, defined in `internal/voicestate` (`PhaseListening`, `PhaseHearing`, `PhaseThinking`, `PhaseSpeaking`, `PhaseResting`, `PhaseOff`). The daemon's raw words map to them with `voicestate.MapPhase`: `transcribing` is `hearing`; `muted`, `paused` (call) and `meeting` are `resting` (the mic is not taking commands; `muted` is also its own field).
- `from` is `local` (current.json), `mirror` (mirror.json) or `none`. `target.host` is `""` for this machine, else the remote name.
- `source` (mac, glasses, phone) is empty until the daemon's state file carries it.
- No live voice (no file, `on: false`, dead daemon, stale file) prints `{"on":false,"phase":"off",…,"from":"none"}` and exits 0.

## needs-me

Sessions whose status is `waiting`, longest first, across every local profile and every configured remote (`[remotes.*]`), asked in parallel. Each host gets `--timeout` (default 3 s); an unreachable or slow host is an entry in `errors`, never a failure. Read-only.

```json
{"count":1,"sessions":[{"id":"…","title":"…","group":"…","profile":"default","host":"","status":"waiting",
  "substate":"interactive-menu","reason":"asks a question","since":"2026-11-17T22:58:00Z","waiting_seconds":123}],
 "errors":[{"host":"deckbox","error":"timeout"}]}
```

`reason` is one line from the substate (and its detail for limits). `since` is the session's last activity time; both it and `waiting_seconds` are empty/0 when the host does not report it. Plain text prints one line per session (`title [host]: reason, waiting 2m`); errors go to stderr.

The Go entry point is `needsme.Collect(ctx, needsme.Options{Sources: needsme.DefaultSources(self, profile)})` in `internal/needsme`.
