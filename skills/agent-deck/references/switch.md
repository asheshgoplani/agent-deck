# Switch to a session by name (voice and chat)

Use when the user says "go to X", "switch to X", "open X session", "take me to X", or in Hindi/Urdu "X par jao", "X pe chalo", "X par switch karo", "X पर जाओ", "X पे चलो", where X names a session. Not for switching a session's account or harness (that is `agent-deck session switch`).

1. Take the session name X from what the user said. Drop filler: "the", "session", "please", "wala", "wali", "वाला", "वाली", "par", "pe", "पर", "पे", "jao", "chalo", "switch karo", "go to", "switch to". Keep every number exactly as said.
2. Run:

   ```bash
   agent-deck switch "X" --json
   ```

3. Read `status` in the JSON and answer in one short line:
   - `switched`: "On <session.title>."
   - `queued`: "<session.title> will show when the app connects."
   - `ambiguous` (exit 3): ask "Which one: <A> or <B>?" with the `candidates` titles (at most five). Run step 2 again with the title the user picks.
   - `not_found` (exit 2): say no session is called X. Run `agent-deck list --json` and name up to three titles that look closest, then ask.

## Rules

- Never guess. If the JSON is not `switched` or `queued`, ask; do not pick a session yourself.
- Numbers must match exactly: "0956" is not "0955". Never change a number to find a match.
- Do not pass `--no-attach` or drop `--json`; the JSON path never takes over the terminal.
- Keep replies short and plain. No ids unless the user asks.
