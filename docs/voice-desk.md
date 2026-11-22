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
| `--on` | The session the mic is on (its id). Absent means none (the daemon's own id can be null): every `tell` then needs a yes. When that id is on two listed sessions (this machine and a remote), a `tell` to either needs a yes too. The desk never guesses it from the voice state. |
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
| `needs_me` | "who is waiting for me", "who needs me", "what needs me" | "kaun mera intezaar kar raha hai", "kisko meri zaroorat hai" | waiting sessions of this profile and the remotes |
| `status` | "status of X", "how is X doing", "what is X doing" | "X ka status kya hai", "X kya kar raha hai" | "X is working, 4 minutes in. Last: …" |
| `read` | "read X", "read the last reply of X", "what did X say" | "X ka jawab padho", "X ne kya bola" | the short part of X's last reply (`<speak>` part, else 2 sentences / 240 characters, no markdown or code) |
| `tell` | "tell X to …", "ask X to …", "tell X that …", "ask X whether …" | "X ko bolo ki …", "X ko bolo …", "X se kaho ki …" | sends the message, after a yes (below) |
| `find` | "find the X session", "find X"; not from the Mac (`--from mac`, the daemon switches itself): "switch to X", "move to X", "go to X" | "X wali session dhoondo", "X dhoondo"; not from the Mac: "X pe jao", "X par chalo", "X pe switch karo" | one hit: "Found X. Switch to it?"; several: "Two: a, b. First or second?". Only sessions of this profile (and the remotes), never a conductor |
| `overview` | "how is everything", "how are things" | "sab kaisa chal raha hai", "sab theek hai" | "Six working, three waiting for you, one failed: audit." |
| `list` | "list my sessions", "what sessions do I have" | "meri sessions batao", "meri sessions sunao" | three names, then "and N more on screen" |

A host can be named: "status of conductor on boxone", "boxone wala conductor ka status batao".

### Answers to a question (the one list for every caller)

Only with `--pending`, and only as the whole sentence (case, punctuation and a leading wake name ignored). The voice
daemon, the phone and the Mac app use this list; `internal/voicedesk/answer.go` is its source.

| Answer | English | Roman Hindi | Devanagari / Urdu |
|---|---|---|---|
| yes | yes, yes send, yes send it, yes please, yes switch, yes do it, send it, okay, ok, ok send, okay send it, yeah, yep, sure, go ahead, do it | haan, haa, han, haan ji, han ji, ha ji, hanji, haanji, ji haan, ji han, ji, haan bhejo, haa bhejo, han bhejo, haan bhej do, haa bhej do, han bhej do, haan bhejdo, bhej do, bhejdo, bhejo, haan haan, theek hai, thik hai, kar do, haan kar do | हाँ, हां, हाँ भेजो, हां भेजो, हाँ जी, जी हाँ, भेज दो; ہاں, جی ہاں, ہاں بھیجو, ہاں جی |
| no | no, cancel, no thanks, nope, stop it | nahin, nahi, nahee, rehne do, rahne do, rehene do, mat bhejo, mat karo, nahin bhejo | नहीं, नहि, रहने दो, मत भेजो; نہیں, رہنے دو, مت بھیجو |
| choice | first, the first, first one, the first one; second …; third … | pehla, pahla, pehla wala, pahla wala, pehli, pahli, pehli wali; doosra, dusra, doosra wala, doosri; teesra, tisra, teesra wala, tisra wala, teesri, tisri | पहला, पहली, पहला वाला; दूसरा, दूसरी; तीसरा, तीसरी |

Matching is exact against these rows: the sentence's words, lowercased, punctuation and a leading wake name
dropped, must equal a row. There is no fuzzy or phonetic matching for answers ("sorry" is not "sure", "ha" is not
"haa", "car do" is not "kar do"); every accepted spelling is a row here. A session's exact name also answers a
choice question. Not answers: a sentence with any question mark anywhere (?, ？, ﹖, ؟, ⁇, ‽, ¿, ❓ …, also next to
zero-width or direction marks: "yes?", "Yes?!", "ہاں؟"), a yes inside a longer sentence ("yes I think so, also …"). A short sentence (three words or fewer) that is neither an answer nor a desk command keeps the question
open once ("Please say yes or no.", new token in `confirm`); a second short miss cancels it ("Cancelled, nothing
sent.", `handled:true`, so it is typed nowhere); a longer sentence drops the token and is handled as a fresh
sentence.

Forms that overlap with plain talk or with the voice daemon's own commands ("read this", "what is he doing",
"find the bug in the parser") come back `handled:false` when the name is not a session. So does any desk form, a
tell included, whose unknown name holds an ordinary coding word ("tell git to ignore the build folder", "ask the
database whether the row exists", "status of the pull request"), and a name made only of coding words that does not
say a session's whole title ("tell the build to …"); a pronoun or generic addressee ("ask him to wait", "tell the
user to …", "usko bolo …") as well. Otherwise a tell to a name always answers: one match reads back, a name that fits
two or more of his sessions asks which one ("tell conductor to …", "status of billing"), and a real-looking unknown
name ("tell phone walk to continue", "tell zephyr to stop", a name with a number) answers "I found no session of
yours called …"; it is never typed into the session as plain text.

## Phase 2: acting on sessions

| Intent | English | Roman Hindi | Does (after a yes where marked) |
|---|---|---|---|
| `launch` | "start a new session called X in G", "launch a new session named X" | "ek nayi session shuru karo naam X", "G mein nayi session shuru karo naam X" | yes: `launch -t x -c <tool> -g G <folder>` (or on a remote) |
| `stop` / `restart` / `archive` | "stop X", "restart X", "archive X" | "X band karo", "X restart karo", "X archive karo" | yes: `session stop|restart|archive`; a conductor (renamed too), the session the mic is on, or a session of another profile is refused at once, no question: "X is a conductor; do that on screen." |
| plural | "stop all workers" | "sab workers band karo" | names every session it will act on, at most five per yes; leaves conductors and the session the mic is on out unless named; "No workers are running." when none is |
| `search` | "where did we talk about X", "search what was said about X" | "X ki baat kahan hui thi" | `recall search` (read-only; needs `[recall] enabled`); only hits from his own sessions of this profile (and the remotes) are spoken or shown |
| `workers` | "what are X's workers doing" | "X ke workers kya kar rahe hain" | `session children` (this machine); only his own sessions are listed |
| `next` | "take me to the next one", "next waiting", "next one", "next session", "go to next waiting" | "agla wala kholo", "agla waiting" | switch to the longest-waiting session he is not on, of this profile (or a remote), never a conductor: "Switching to X, it is waiting for you." |
| `refuse` | "delete X", "remove the X session", "delete all workers", "delete everything", "stop X and delete it", "I want to delete X", "purge X", "throw away X" | "X hata do", "X ko mita do", "sab workers hata do", "sab hata do", "X band karo aur hata do", "mujhe X delete karna hai" | nothing: "I never delete a session by voice; do it on screen." |
| `todo_add` | "add a to-do: X", "put X on my list", "remind me to X" | "to-do mein daalo: X", "X list mein daal do" | `todo add`; "add this to my list" and "add a todo comment" are talk |
| `todo_list` | "what is on my list", "read my to-dos" | "meri list sunao", "mere to-do batao" | `todo list`; "what is left to do" is talk |
| `todo_done` | "tick off X", "mark X done", "X is done", "the X one is done" | "X wala ho gaya", "X ho gaya" | the whole item ticks at once; part of it after "tick off" / "wala" / "the X one" asks "Tick off fix the build?" (several: "Did you mean A or B?"); a plural is the same word ("test"/"tests"; "es" only after s, x, z, ch, sh, as `todo done`: "jon" is not "jones"); a close word ("plan" for "plane", "rent" for "rental", "jon" for "jones") changes nothing: "No to-do is called plan; say its whole name."; a bare "the build is done" / "build ho gaya" is talk |

Every launch, stop, restart and archive is read back and needs a yes within 10 s; the yes re-checks each session.
A launch starts only in the group's own default folder (`group update G --default-path ...`) and says it with the
tool: "Start a new session x in G, in folder alpha, with claude? Yes?". A group without one is refused; the folder is
never taken from what was heard. A name is plain words only: every word starts with a letter or a digit (no
"-p", "--cmd", dash or fullwidth look-alikes); anything else is "A session name can only use plain words; type it
instead.". The tool is `default_tool` from config.toml when it is a plain name, else claude.
An unknown name after stop/restart/archive is not the desk's ("stop listening" belongs to the voice daemon).
A sentence with a delete verb (delete, remove, erase, wipe, drop, purge, scrap, discard, throw away, get rid of,
hata, hatwa, mita, mitwa, nikal, uda, khatam, saaf, phenk, their inflected forms such as "deleted", "deleting",
"hataiye", and their Devanagari and misheard forms) is refused when what is left names a session (his, someone
else's, or one of nobody's said by its own words), a group of his, everything ("delete everything", "delete all
sessions", "sab hata do"), or a short name with a number ("delete test 1", "delete worker one"); polite and
who-does-it words ("please", "I want you to", "go ahead and", "now", "for me", "from the deck", "mujhe ... hai") are
dropped first. It never reaches the session the mic is on. A delete that names
no session is talk ("delete the session cookie", "remove the unused import", "hata do ye function").
"Open the page from X" opens a page only when X is one of his sessions word for word; anything else
("open the coverage report", "open the test report") is talk.

## Safety

1. **Only the owner's sessions** (`[voice]` in config.toml, see the config reference). Another person's session is
   refused by name ("That is Sam's session.") and never acted on. Every spoken name word must be in the title or
   group (two spoken words may make one title word: "mac app" → `macapp`); the switch matcher's number rule holds
   ("audit 0956" is not `audit-0955`). A number word after a name word is its digits: "worker one" is `worker-1`,
   never `worker-11`; one to twenty, and the Hindi ek to das ("worker do" is `worker-2` unless a session has the
   word "do" in its name). A name that only looks like a title (edit distance) is never silently the
   target: the desk asks "Did you mean voice test?". Several equal hits: "Did you mean A or B?". Archived sessions
   are never targets.
2. **Never another profile, never a conductor.** The desk lists only the profile it runs in (`-p`), plus the
   configured remotes, so a session of another local profile is never named, found, switched to or picked. A
   conductor (the deck's conductor flag, carried as `conductor_name` in `list --json`, so a renamed conductor
   still counts; for an older host, a `conductor-<name>` title or the `conductor` group) is never found, switched
   to or picked, his or anyone's. Every switch checks both again right before it acts (also after a yes, on the
   session as it is then): "<title> is a conductor; voice does not switch to conductors. Nothing done." /
   "<title> is in another profile; nothing done.".
3. **Say it back**: "Sent to X." after a send; the read-back before it.
4. **A yes before `tell`** unless the target is the session the mic is on, and always when the message holds a risky
   word (delete, remove, push, merge, release, install, restart, pay, mail; Roman/Devanagari forms such as "hata do",
   "mita do", "mail bhej do"). Prompt: "Send to X: <message>. Yes?". A yes more than 10 s after the question
   (`--answer-delay-ms` > 10000, or a token older than 10 s when the flag is absent) sends nothing: "Too late,
   nothing sent.". Tokens die after 60 s whatever the delay says, are single-use (claimed by an atomic rename), and
   are bound to the profile and to the exact action by a hash (an unkeyed hash: it catches a stale or edited file,
   not someone who can already write the 0700 directory). A stopped or failed session is not sent to. A session
   that shows a permission menu or question (substate `interactive-menu`) is not sent to: "<title> is asking a
   question; answer it on screen first."; sends into agent tools also use `session send --require-input-prompt`,
   so nothing is typed unless an input prompt is visible. The yes re-reads only the target (its own host), and
   says the title it has now. A send that stopped before typing says "Could not send to <title>."; a target that
   shows no input prompt says "<title> is not at its prompt, so nothing was sent"; only when the outcome is not
   known (time ran out after typing began, or the send could not confirm it) the line is "I am not sure it was
   sent; check <title>." Never a failure or a success that did not happen.
5. **Unreadable settings refuse.** A `config.toml` that cannot be parsed never falls back to the open default: the
   desk answers `refused:"unsafe"`, "I cannot read the voice settings.", and acts on nothing.
6. **The message is cleaned**: every control character (newline, tab, escape, Ctrl-C …) becomes a space before
   it is read back, stored or sent, so a typed message cannot become keystrokes or a second command line.
7. **No secrets spoken or shown**: replies and messages are checked for keys, tokens and passwords (a pattern list,
   a key word followed by a value that is not an ordinary word, and a high-entropy check that leaves identifiers,
   commit hashes and digests alone). On any hit nothing of the text is in `show`. A reply with one: "There is a secret in this reply, read it on screen.", and nothing of
   it in `show`. A message with one is not sent by voice.
8. **Short**: one sentence (two for a status); lists speak three names.
9. **The mic marker is not a name**: the voice daemon's "🎙 " title prefix is stripped from everything the desk says, shows, returns as `target` or matches.
10. **No logs**: the desk writes no heard or spoken text to any log. Pending questions live in
   `<profile dir>/voice/pending/<token>.json` (0600, directory 0700) until answered; every `voice do` call (also
   one that is not the desk's) removes questions older than 60 s. Note: `session send` itself records every sent
   message in its trigger ledger (`state.db`, `trigger_fires`), for voice sends as for typed ones.

## How it acts

The desk lists sessions through the same sources as `agent-deck needs-me`, but only this profile's `list --json` (`-p`), and every
configured remote's session list, in parallel with a per-host timeout; an unreachable host is said, not fatal). It
acts through the commands a person would type: `session output --json` and `session send --message-file - --no-wait`
for this machine, `remote <name> output` and `remote <name> send` for a remote. The message goes on stdin, never on
a command line. A local switch is delivered like `agent-deck switch` (Mac app URL scheme on the Mac, else a
`macapp.switch` frame on the events bus); a remote switch is left to the caller (`then`).
