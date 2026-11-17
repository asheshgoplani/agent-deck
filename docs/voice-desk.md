# Voice desk: `agent-deck voice do`

The voice desk turns one heard sentence into a deck action and returns the short line to speak. A voice daemon, a
phone app or a person typing hands it every sentence it would otherwise type into a session; the desk answers
"not mine" (`handled:false`) in a few milliseconds for anything that is not one of its commands.

```
agent-deck [-p <profile>] voice do [--json] [--dry-run] [--from mac|glasses|phone|text] [--on <session id>]
                                   [--pending <token>] [--answer-delay-ms <n>] "<sentence>"
```

Callers always pass `--` before the sentence (`voice do --json --from mac -- "<sentence>"`), so a sentence that
starts with `-` is never read as a flag.

Exit codes: 0 = a result was printed (handled or not, refused or not), 2 = usage error, 1 = internal error. Without
`--json` only the line to speak is printed (nothing when the sentence is not the desk's).

| Flag | Meaning |
|---|---|
| `--json` | Print one JSON object (below) and nothing else on stdout. |
| `--dry-run` | Parse, resolve and apply the safety rules; never send, never switch, never store a question. `dryRun:true`. |
| `--from` | Where the sentence came from (`text` by default). |
| `--on` | The session the mic is on. Absent means none (the daemon's own id can be null): every `tell` then needs a yes. The desk never guesses it from the voice state. |
| `--pending` | The token of the question the desk asked last; the sentence is the answer. |
| `--answer-delay-ms` | Silence between the end of speaking the question and the start of the answer. |

## Result

```
{
  "handled": true,               // false: type the sentence into the session as before; say is ""
  "intent": "needs_me|status|read|tell|find|overview|list|confirm_yes|confirm_no|choice|none",
  "lang": "en|hi",
  "target": {"id","title","host","profile"} | null,  // host "" = this machine, else the remote's name
  "say": "Sent to deck conductor.",                   // never empty when handled
  "show": {"kind": "sessions|confirm|none", "items": [{"id","title","host","status","line"}]},
  "confirm": {"token","kind":"yes|choice","prompt","expiresMs":10000} | null,
  "then": {"action":"switch","target":{"id","title","host"}} | null,
  "refused": "other-person|not-found|ambiguous|unsafe|expired" | null,
  "dryRun": false
}
```

Caller rules: `handled:false` → behave as before. `handled:true` → speak `say`, never type the sentence anywhere.
`confirm` → pass the next heard sentence with `--pending <token> --answer-delay-ms <ms>`; after `expiresMs` of
silence drop the token. `then.action == "switch"` → move the mic to that session (the desk has already asked the Mac
app to show a local one).

## What it understands

English, Roman Hindi as people write it, and Hindi in Devanagari as some listeners write it. Hindi filler and the wake
name are dropped; Roman and Devanagari spellings are folded before comparing (`intezaar`/`intazaar`/`इंतज़ार`).

| Intent | English | Roman Hindi | Does |
|---|---|---|---|
| `needs_me` | "who is waiting for me", "who needs me", "what needs me" | "kaun mera intezaar kar raha hai", "kisko meri zaroorat hai" | waiting sessions, all profiles and remotes (the `needs-me` list) |
| `status` | "status of X", "how is X doing", "what is X doing" | "X ka status kya hai", "X kya kar raha hai" | "X is working, 4 minutes in. Last: …" |
| `read` | "read X", "read the last reply of X", "what did X say" | "X ka jawab padho", "X ne kya bola" | the short part of X's last reply (`<speak>` part, else 2 sentences / 240 characters, no markdown or code) |
| `tell` | "tell X to …", "ask X to …", "tell X that …", "ask X whether …" | "X ko bolo ki …", "X ko bolo …", "X se kaho ki …" | sends the message, after a yes (below) |
| `find` | "find the X session", "find X" | "X wali session dhoondo", "X dhoondo" | one hit: "Found X. Switch to it?"; several: "Two: a, b. First or second?" |
| `overview` | "how is everything", "how are things" | "sab kaisa chal raha hai", "sab theek hai" | "Six working, three waiting for you, one failed: audit." |
| `list` | "list my sessions", "what sessions do I have" | "meri sessions batao", "meri sessions sunao" | three names, then "and N more on screen" |

A host can be named: "status of conductor on boxone", "boxone wala conductor ka status batao".

### Answers to a question (the one list for every caller)

Only with `--pending`, and only as the whole sentence (case, punctuation and a leading wake name ignored). The voice
daemon, the phone and the Mac app use this list; `internal/voicedesk/answer.go` is its source.

| Answer | English | Roman Hindi | Devanagari / Urdu |
|---|---|---|---|
| yes | yes, yes send, yes send it, yes please, yes switch, yes do it, send it, okay, ok, ok send, okay send it, yeah, yep, sure, go ahead, do it | haan, haa, han, haan ji, han ji, ji haan, ji han, ji, haan bhejo, haa bhejo, haan bhej do, bhej do, bhejdo, bhejo, haan haan, theek hai, thik hai, kar do, haan kar do | हाँ, हां, हाँ भेजो, हां भेजो, हाँ जी, जी हाँ, भेज दो; ہاں, جی ہاں, ہاں بھیجو, ہاں جی |
| no | no, cancel, no thanks, nope, stop it | nahin, nahi, nahee, rehne do, rahne do, mat bhejo, mat karo, nahin bhejo | नहीं, नहि, रहने दो, मत भेजो; نہیں, رہنے دو, مت بھیجو |
| choice | first, the first, first one, the first one; second …; third … | pehla, pehla wala, pehli, pehli wali; doosra, dusra, doosra wala, doosri; teesra, tisra, teesra wala, teesri | पहला, पहली, पहला वाला; दूसरा, दूसरी; तीसरा, तीसरी |

Also: a session's name answers a choice question; a word of four letters or more may be spelled the way the
listener writes it ("rahne do", "pahla"); a short word must be exact ("ho" is never "haan"). Not answers: a sentence
ending in "?" ("yes?"), a yes inside a longer sentence ("yes I think so, also …"). A short sentence (three words or
fewer) that is neither an answer nor a desk command keeps the question open once ("Please say yes or no.", new
token in `confirm`), so it is not typed into the session; a second miss, or a longer sentence, drops the token and
is handled as a fresh sentence.

Forms that overlap with plain talk or with the voice daemon's own commands ("read this", "what is he doing",
"find the bug in the parser") come back `handled:false` when the name is not a session. So does any desk form whose
unknown name holds an ordinary coding word ("tell git to ignore the build folder", "status of the pull request"),
and a name made only of coding words that does not say a session's whole title ("tell the build to …"). A short
unknown name in an explicit form ("tell phone walk to continue") answers "I found no session of yours called …".

## Phase 2: acting on sessions

| Intent | English | Roman Hindi | Does (after a yes where marked) |
|---|---|---|---|
| `launch` | "start a new session called X in G", "launch a new session named X" | "ek nayi session shuru karo naam X", "G mein nayi session shuru karo naam X" | yes: `launch -t x -g G` (or on a remote) |
| `stop` / `restart` / `archive` | "stop X", "restart X", "archive X" | "X band karo", "X restart karo", "X archive karo" | yes: `session stop|restart|archive` |
| plural | "stop all workers" | "sab workers band karo" | lists first, at most five per yes |
| `search` | "where did we talk about X", "search what was said about X" | "X ki baat kahan hui thi" | `recall search` (read-only; needs `[recall] enabled`) |
| `workers` | "what are X's workers doing" | "X ke workers kya kar rahe hain" | `session children` (this machine) |
| `next` | "take me to the next one" | "agla wala kholo" | switch to the first waiting session he is not on |
| `refuse` | "delete the X session" | "X session delete karo" | nothing: voice never deletes, pushes or pays |

Every launch, stop, restart and archive is read back and needs a yes within 10 s; the yes re-checks each session.
An unknown name after stop/restart/archive is not the desk's ("stop listening" belongs to the voice daemon).

## Safety

1. **Only the owner's sessions** (`[voice]` in config.toml, see the config reference). Another person's session is
   refused by name ("That is Sam's session.") and never acted on. Every spoken name word must be in the title or
   group (two spoken words may make one title word: "mac app" → `macapp`); the switch matcher's number rule holds
   ("audit 0956" is not `audit-0955`). A name that only looks like a title (edit distance) is never silently the
   target: the desk asks "Did you mean voice test?". Several equal hits: "Did you mean A or B?". Archived sessions
   are never targets.
2. **Say it back**: "Sent to X." after a send; the read-back before it.
3. **A yes before `tell`** unless the target is the session the mic is on, and always when the message holds a risky
   word (delete, remove, push, merge, release, install, restart, pay, mail; Roman/Devanagari forms such as "hata do",
   "mita do", "mail bhej do"). Prompt: "Send to X: <message>. Yes?". A yes more than 10 s after the question
   (`--answer-delay-ms` > 10000, or a token older than 10 s when the flag is absent) sends nothing: "Too late,
   nothing sent.". Tokens die after 60 s whatever the delay says, are single-use (claimed by an atomic rename), and
   are bound to the profile and to the exact action by a hash (an unkeyed hash: it catches a stale or edited file,
   not someone who can already write the 0700 directory). A stopped or failed session is not sent to. A session
   that shows a permission menu or question (substate `interactive-menu`) is not sent to: "<title> is asking a
   question; answer it on screen first."; sends into agent tools also use `session send --require-input-prompt`,
   so nothing is typed unless an input prompt is visible. The yes re-reads only the target (its own host), and
   says the title it has now. When a send's outcome is not known (time ran out after it started) the line is "I
   am not sure it was sent; check <title>.", never a failure or a success that did not happen.
4. **Unreadable settings refuse.** A `config.toml` that cannot be parsed never falls back to the open default: the
   desk answers `refused:"unsafe"`, "I cannot read the voice settings.", and acts on nothing.
5. **The message is cleaned**: every control character (newline, tab, escape, Ctrl-C …) becomes a space before
   it is read back, stored or sent, so a typed message cannot become keystrokes or a second command line.
6. **No secrets spoken or shown**: replies and messages are checked for keys, tokens and passwords (a pattern list,
   a key word followed by a value that is not an ordinary word, and a high-entropy check that leaves identifiers,
   commit hashes and digests alone). On any hit nothing of the text is in `show`. A reply with one: "There is a secret in this reply, read it on screen.", and nothing of
   it in `show`. A message with one is not sent by voice.
7. **Short**: one sentence (two for a status); lists speak three names.
8. **The mic marker is not a name**: the voice daemon's "🎙 " title prefix is stripped from everything the desk says, shows, returns as `target` or matches.
9. **No logs**: the desk writes no heard or spoken text to any log. Pending questions live in
   `<profile dir>/voice/pending/<token>.json` (0600, directory 0700) until answered; every `voice do` call (also
   one that is not the desk's) removes questions older than 60 s. Note: `session send` itself records every sent
   message in its trigger ledger (`state.db`, `trigger_fires`), for voice sends as for typed ones.

## How it acts

The desk lists sessions through the same sources as `agent-deck needs-me` (every local profile's `list --json`, every
configured remote's session list, in parallel with a per-host timeout; an unreachable host is said, not fatal). It
acts through the commands a person would type: `session output --json` and `session send --message-file - --no-wait`
for this machine, `remote <name> output` and `remote <name> send` for a remote. The message goes on stdin, never on
a command line. A local switch is delivered like `agent-deck switch` (Mac app URL scheme on the Mac, else a
`macapp.switch` frame on the events bus); a remote switch is left to the caller (`then`).
