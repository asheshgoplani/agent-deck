# To-dos

A small to-do list inside agent-deck for the owner: one list per host, shared by every
profile, the same in the CLI, the TUI, the web API (phone gateway, Mac app) and voice.

**One host, one list.** The list lives on the host the clients read (the deck host: the
one the phone gateway and the Mac app talk to, and where voice runs `agent-deck todo`).
Another host has its own separate list; nothing syncs lists between hosts.

## Store

- File: `<agent-deck data dir>/todos.json` (`~/.local/share/agent-deck/todos.json` on the
  XDG layout), mode 0600.
- Writes run under the config-file lock (`todos.json.lock`) and replace the file by
  rename (`internal/atomicfile`, fsynced), so two processes never lose each other's change,
  a reader never sees half a file and a crash or power loss after a change keeps it.
  Readers take no lock.
- A file written by a newer agent-deck (`version` above 1) is read but never rewritten:
  changes are refused with "the to-do list was written by a newer agent-deck" (CLI exit 1,
  web `409`), so fields this build does not know are not dropped.
- Text is cleaned on the way in and again on the way out (a hand edit, an older writer):
  whitespace runs fold to one space; control characters and format characters (bidi
  overrides such as U+202E, zero-width characters, the BOM) are dropped.
- A corrupt file is an error, never silently replaced by an empty list.
- Done items are dropped 30 days after they were done (on the next write).
- Package: `internal/todos` (`Store.Add/List/Done/Undo/Remove`, `Match`, `Publish`).

## Item

```json
{"id":"3","text":"call the bank","created_at":"2026-11-17T21:40:00Z",
 "done_at":null,"source":"voice","session_id":"abc123"}
```

| Field | Meaning |
|---|---|
| `id` | short decimal string, never reused (removing 3 does not free 3) |
| `text` | at most 500 characters; whitespace runs (newlines too) fold to one space, control characters are dropped |
| `created_at` | RFC 3339, UTC, seconds |
| `done_at` | RFC 3339, or `null` while open (always present) |
| `source` | `voice`, `cli`, `phone`, `mac` or `tui` |
| `session_id` | optional; letters, digits, `-`, `_`, `.`, at most 128; only present when set |

## CLI

```
agent-deck todo add "<text>" [--source s] [--session id] [--json]
agent-deck todo list [--all] [--json]
agent-deck todo done <id | words of the text> [--source s] [--json]
agent-deck todo undo <id> [--source s] [--json]
agent-deck todo remove <id> [--source s] [--json]
```

`add` joins its words, so `agent-deck todo add call the bank` works too. `--source`
defaults to `cli`. `list` shows open items oldest first; `--all` adds done items after them.

### Done by words

`done` with anything but a number matches words of the items' text. The query is split
into words: runs of letters, digits and combining marks, lowercased, so a Devanagari word
keeps its vowel signs and virama and stays one word (`बैंक` never matches `किताब`). `the`,
`a`, `an`, `my`, `todo` and `to-do` (or `to do`) are dropped; a query of only those words
names nothing (exit 2). An item is a candidate when **every** query word matches one of its
words: the same word, or the same word with a plural or inflection ending `s`, `es`, `ें` or
`ों`, either way round (`bank` or `banks` for `call the bank`, `egg` for `buy eggs`, `tax`
for `pay taxes`, `किताबें` for `किताब खरीदो`; the bare word has at least 3 letters).

A word of 4 or more letters that only starts an item's word at most 2 letters longer is a
**close match**, never a match: `plan` for `book the plane`, `pay rent` for
`pay rental insurance`, `read` for `get ready for demo`, `port` for `check the portal`.
When no open item matches but open items match closely, nothing is ticked off: the
command exits 3 with status `close_match` and those items as candidates, so the voice
desk asks "Did you mean book the plane?" and, on a yes, ticks it off by id. Anything else
is a look-alike and never matches: a short prefix (`car` for `card`), a long tail (`bank`
for `bankruptcy`) or a one-letter difference (`maria` for `mario`, `dentsit` for
`dentist`); the caller asks again instead of ticking off the wrong item.

- an exact match comes first (the item's words equal the query's, in order): one open
  exact match wins over longer candidates (`buy milk` against `buy milk` and
  `buy milk and bread`); when no open item is exact but a done one is, the answer is
  `already_done` for that item, so saying `done pay rent` twice never ticks off
  `pay rental insurance`;
- otherwise one open candidate is ticked off (a match beats a close match: `plan` ticks
  off `plan the trip` even when `book the plane` is open); several change **nothing** and
  the command exits 3 with the candidates. It never guesses;
- no open candidate but exactly one done candidate: `already_done` for it, so repeating
  `done bank` is not "no such to-do";
- no candidate at all, but open close matches: `close_match`, exit 3, nothing changed
  (a done item that only looks alike is not reported);
- none: exit 2.

A number is only ever an id (`12`, `#12`, `12.`): when there is no item 12 it exits 2,
even if an item's text contains "12".

Text that starts with `-` goes after `--`; everything after `--` is text, so flags go
before it: `agent-deck todo add --json -- "-5 degrees check"`.

`--source` on `done`, `undo` and `remove` (default `cli`) is reported as `by` in the
`todo.changed` event, so the voice desk passes `--source voice`.

### Exit codes

`0` ok (also for `already_done` / `already_open`), `1` error (bad input, unreadable list),
`2` no item matches, `3` more than one item matches (`ambiguous`) or only a close match
(`close_match`); with `3` nothing was changed and the candidates are in the answer.

### JSON (one object on stdout)

| Command | Object |
|---|---|
| `list` | `{"todos":[item...],"open":2,"done":1}` (`todos` is `[]` when empty) |
| `add` | `{"status":"added","todo":item,"open":2}` |
| `done` | `{"status":"done"\|"already_done","todo":item,"open":1}` |
| `undo` | `{"status":"reopened"\|"already_open","todo":item,"open":2}` |
| `remove` | `{"status":"removed","todo":item,"open":1}` (the item as it was) |
| no match | `{"status":"not_found","query":"bank"}` (exit 2) |
| ambiguous | `{"status":"ambiguous","query":"milk","candidates":[item...]}` (exit 3) |
| close match | `{"status":"close_match","query":"plan","candidates":[item...]}` (exit 3; ask "Did you mean ...?", then `done <id>`) |
| error | `{"status":"error","error":"..."}` (exit 1; also for a bad flag when `--json` was given; the message is on stderr too) |

## Event

Every change publishes one `todo.changed` frame on the events bus of **every profile on
the host** (and the caller's profile), so a screen following any profile sees it. The
frame never carries the item's text: the bus log is kept on disk in every profile, and no
heard or typed text belongs in a log. A screen that sees the frame re-reads the list
(`agent-deck todo list --json` or `GET /api/todos`):

```json
{"kind":"todo.changed","session_id":"<item's session id or empty>",
 "data":{"action":"add|done|undo|remove","id":"3","open":2,"by":"cli|voice|phone|mac|tui"}}
```

Follow it with `agent-deck events follow --kind todo.`. Publishing never fails a change: a
disabled or unwritable bus is skipped.

## Web API

Next to the other routes of `agent-deck web`, same auth (owner token as bearer header or
cookie; an `adk_` assistant token is refused):

| Route | Body / query | Answer |
|---|---|---|
| `GET /api/todos` | `?all=1` adds done items | `200` list object |
| `POST /api/todos` | `{"text","source"?,"session_id"?}`; `source` defaults to `phone` | `201` add object; `400` bad text, source or session id |
| `POST /api/todos/{id}/done` | `?source=mac` (default `phone`, used as `by` in the event) | `200` done / already_done object; `400` id not a number; `404` no such id |
| `POST /api/todos/{id}/undo` | as for done | `200` reopened / already_open object (a wrong tap reversed); `404` no such id |
| `POST /api/todos/{id}/remove` | as for done | `200` removed object (the item as it was); `404` no such id |

Writes are refused with `403 READ_ONLY` when the server runs `--read-only`, with
`403 MUTATIONS_DISABLED` when web mutations are off, and share the mutation rate limit
(`429`). As for every write route, a server with a token refuses a write without a
same-origin `Origin` or `Referer` header (`403 CROSS_ORIGIN_BLOCKED`), so a gateway must
send one. A file written by a newer agent-deck answers `409`. Words matching is CLI and
voice only; the API changes items by id.

## TUI

`alt+o` (rebind with `[hotkeys] todos = "..."`) opens the to-dos panel: the open items,
`j`/`k` to move, `x` (or space, enter) to tick off, `a` to add (source `tui`), `tab` to show
or hide done items (`x` on a done item reopens it), `esc`, `q` or the key that opened it
to close. On a Mac, `alt+o` is Option+o only when the terminal sends Option as Meta
(Terminal: "Use Option as Meta key"; iTerm2: Left Option key "Esc+"); otherwise rebind
it. A long list scrolls: the window keeps the selected row on screen and says how many
items are above and below. While open it re-reads
the list every 2 s, so items added by voice, the phone or the CLI appear on their own.
Writes run off the screen's update loop (they may wait for the file lock), so the TUI never
freezes on a busy list. Removing is CLI only.
