---
name: six-gates
description: Artifact-first acceptance gating for one new feature, before it ships. Use when a feature is about to be called "done", when you are writing a worker brief for a feature or fix, when a reviewer asks "how do you know this works?", or when someone says "verify this feature", "prove it works", "gate this before merge", "write the acceptance script", "run the gates", or "sixgate". Produces six committed artifacts (G0 script, G1 drive transcript, G2 on-screen assertions, G3 matrix, G4 oracle parity, G5 cold-eye report) and a VERDICT that expires when the source changes. NOT for unit tests, and NOT for auditing an existing product's whole capability surface — that is `capability-verification`.
---

# six-gates

**"Done" is not a builder's claim, it is an artifact.**

A feature was once declared done on the strength of code analysis, a 2,961-transcript
corpus replay and an adversarial review. Nobody ever pressed the key. The first thing
its user saw when they opened it was a blank percentage they had to ask about.

Static verification cannot detect *"this feels broken on arrival"*. Only a recording
of the software being used can. SIXGATE is the shape of that recording, and its single
rule is:

> **No transcript, not done.**

## When to use this, and when not to

| Situation | Use |
|---|---|
| One new feature or fix, about to be called done | **this skill** |
| Auditing the product's whole capability surface (229 CAP-IDs, per-surface evidence) | `capability-verification` — the per-*product* sibling of this per-*feature* skill |
| Proving a function returns the right value | an ordinary unit test |

The two verification skills are deliberately different jobs and should cross-reference,
not merge: `capability-verification` asks *"does everything we shipped still work?"*;
this asks *"is this one thing actually finished?"*. Load whichever question you are
being asked.

---

## 🛡️ HARD SAFETY — non-negotiable, read before running anything

`~/.agent-deck` is BOTH the agent-deck git checkout AND the user's live data (profiles,
config, `state.db`, conductors, dozens of live tmux sessions). Careless runs have wiped
the profile index three times and destroyed the entire session fleet twice.

- **NEVER run `go test` on any agent-deck package against a real `$HOME`.** SIXGATE's
  gate runners are standalone binaries precisely so they never need a test runner.
- **NEVER write to `/Users/<user>/.agent-deck`.** Read-only reads are fine. Never point
  a dev binary at the live `state.db` in any mode that could migrate or write it.
- **Sandbox every command:**
  ```sh
  HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= \
  AGENTDECK_PROFILE= GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" <cmd>
  ```
- **tmux is the danger.** Any harness that spawns tmux MUST:
  - use a **deterministic dedicated socket** (`tmux -L ctxgate-<runid>`), never the
    default socket and never the user's;
  - register teardown that runs `kill-server` **with the same socket and environment
    resolution as the spawn** — an env mismatch makes `kill-server` a silent no-op;
  - never `RemoveAll` a socket directory before killing its server;
  - **NEVER identify tmux processes by name or argv.** On macOS a process keeps its
    original argv; that is what killed the fleet on 2026-07-26. Identify by socket path
    only. No `pgrep`, no `pkill`, no `killall`;
  - never touch a socket it did not create, and never anything matching `agentdeck_*`;
  - **verify at the end of every run** that `tmux -L <name> ls` FAILS, and record the
    PTY count before and after.
- **Use `trash`, never `rm`.** Never `claude -p` (billed).
- Static gates only, all sandboxed: `go build ./...`, `go vet ./...`, `gofmt -l`.

`sixgate selfcheck` encodes the tmux rules as a lint over the framework's own source
and fails the build if any of them is violated. Run it before trusting any gate.

---

## The six gates

Each produces a durable **artifact**, not a claim. All of them live in one committed,
diffable tree per feature:

```
docs/gates/<slug>/
  G0-script.yaml                       the journey, authored BEFORE code
  G1-drive/<fixture>/NN-*.screen.txt   what a human would have seen
                     transcript.md     step | key pressed | what appeared
                     run.json          timings, binary sha, PTY census
  G2-assert/results.{json,md}          assertions read the rendered frames only
  G3-matrix/matrix.{json,md}           every world, incl. a mandatory cold first run
  G4-oracle/oracle.yaml                the figures and their tolerances
            parity.{json,md}           ours vs. somebody else's numbers
            must-label.json            unoracled figures G2 must prove are labelled
  G5-coldeye/brief.md, report.md       a reviewer given the binary and one sentence
             resolutions.yaml          every "looked broken" item, closed
             outcome.{json,md}
  VERDICT.{md,json}                    roll-up + source-hash binding
```

| Gate | What it is | Pass criterion |
|---|---|---|
| **G0 SCRIPT** | the journey as literal keystrokes, written **before** building | validates; ≥1 `expect` per capture step; a human reviewed it before any implementation commit |
| **G1 DRIVE** | something executed G0 against the **real built artifact** and captured what a human would see | every step ran; every capture produced a non-empty frame; `pty_delta == 0` |
| **G2 ASSERT** | assertions read the **rendered output**, never internals | every `expect` passes, the Blank Detector is clean, and G4's must-label contract is satisfied |
| **G3 MATRIX** | every tool, every session state, empty and enormous, narrow terminals, **cold first run** | every declared row ran; zero failures; the cold-first-run and real-binary rows cannot be excluded |
| **G4 ORACLE** | every number checked against a truth **the author did not write** | every figure agrees, or drifts with a written explanation, or has no oracle **and G2 proves the UI labels it an estimate** |
| **G5 COLD-EYE** | a reviewer given **only** the binary and one sentence | report exists, brief was not contaminated, every "looked broken" item fixed or accepted in writing |

### The two ideas worth stealing even if you never run this tool

1. **The Blank Detector.** A universal lint applied to every frame in every gate, with
   no opt-in: `( %)`, `()`, `NaN`, `<nil>`, `%!d(MISSING)`, `undefined`, `null`,
   `[object Object]`, `{{ .Name }}`, a bare `--` where a number belongs, a label with
   nothing after it, `0 tokens` beside a category that has items. It catches *"feels
   broken on arrival"* **without anyone knowing to look for it** — which is the entire
   class of failure that produced this framework. Suppressing a rule requires naming
   the frames it covers and writing a justification; a blanket suppression is how a
   blank percentage ships.

2. **Unoracled ⇒ must be labelled.** G4 emits the list of figures nobody has a truth
   source for; G2 turns each into an ordinary on-screen assertion against the recorded
   frames; `sixgate verdict --check` compares the digest G2 obeyed against the file G4
   wrote. "We have no oracle" and "the UI says so, permanently" become mechanically
   coupled. A number with no oracle and no label is a gate failure, not a footnote.

---

## Running it

```sh
sixgate scaffold <slug>                  # writes G0 ONLY. G1..G5 stay locked.
$EDITOR docs/gates/<slug>/G0-script.yaml # write the journey. Get it reviewed.
sixgate scaffold <slug>                  # unlocks G1..G5 once G0 validates
sixgate drive    <slug>                  # G1, in-process model: no tmux, no PTY
sixgate drive-b  <slug>                  # G1/G3, shipped binary in a real pane
sixgate assert   <slug>                  # G2
sixgate matrix   <slug>                  # G3
sixgate oracle compare <slug>            # G4 -> must-label.json
sixgate assert   <slug>                  # G2 again, now bound by G4's contract
sixgate coldeye brief   <slug> -seed <case>   # G5: build the reviewer's world
#   ... send a reviewer, save their report.md, answer every finding ...
sixgate coldeye outcome <slug>           # G5
sixgate verdict  <slug>                  # write the roll-up
sixgate verdict  <slug> --check          # THE GATE. exit 0 == done.
sixgate selfcheck                        # prove the framework's own rules still hold
```

`scripts/run-gates.sh` runs that sequence under the sandbox preamble.
`scripts/coldeye-review.sh` dispatches a genuinely cold reviewer.

**Ordering is enforced, not suggested.** `scaffold` refuses to create `G1-drive/`…
`G5-coldeye/` until G0 validates: you cannot start collecting evidence for a journey
nobody wrote down.

**The verdict expires by itself.** `VERDICT.json` hashes every source file the feature
declares in `owns`. `--check` re-hashes them, so a transcript recorded three commits
ago stops counting as evidence — and a hand-edited `VERDICT.md` fails, because the
check re-renders it from the JSON and byte-compares.

---

## Writing a G0 script

Write it in the words of whoever asked for the feature, before any code exists. If
nobody can write down the keys a human would press and what should appear, the feature
is not defined yet and there is nothing to build.

```yaml
version: 1
slug: context-inspector
sentence: "Open the TUI, press C on a busy session, see how full it is, drill into memory files, find the fat one, exit."
term: {width: 200, height: 50}
owns: [internal/ui/context_pager.go, internal/ctxinspect/...]
steps:
  - id: 04-overview
    do: {wait_for: "[ Overview ]"}
    capture: inspector-overview
    expect:
      - screen_matches: '\(\d{1,3}(?:\.\d)?%\)|context window size unknown, so no percentage is shown'
        why: "the gauge must state how full the window is, or say plainly that it cannot know"
      - screen_not_matches: '\(\s*%\s*\)'
        why: "the literal thing the user saw. Must fail this gate permanently, at any terminal size."
```

**Write the assertion that would have failed the original miss.** Assert *a number, or
the honest sentence* — never assert that something is merely present, because a blank
percentage is present too.

**Expect G1 to correct your script.** On the first real drive of the context inspector,
three things the source reading had confidently got wrong were corrected by one
recorded frame: the drill is an Overview journey not a Breakdown one, the breadcrumb
separator is a chevron, and the harness itself was rendering "no sessions" above a
populated list. Correct the script; never relax an assertion until it passes.

---

## Genericity: what transfers, and what you swap

**Universal — any software, unchanged:** the six-gate contract · the artifact tree,
`transcript.md`, and the source-hash staleness binding · the G0 schema and its verbs ·
the Blank Detector · G2's structural rule that the assert runner **cannot import
product code** (enforced by an allowlist, not a denylist) · the matrix schema and the
cold-first-run mandate · the oracle schema with tolerances, consent gating, and the
unoracled ⇒ must-be-labelled coupling · the cold-eye brief/report protocol · *no
transcript, not done*.

**Swappable adapters:** the **driver** (in-process bubbletea and a real tmux pane here;
`playwright` for a web app, `pty-expect` for a CLI, `maestro`/XCUITest for mobile) ·
the **fixture materializer** · the **oracle source** (`/context` here; a Stripe
dashboard, a SQL query, a competitor's tool, a hand-tallied spreadsheet elsewhere) ·
the tmux discipline, which exists *only* because one driver touches tmux.

A web-app author changes one line — `driver: playwright` — keeps G0/G2/G3/G4/G5
verbatim, and their `.screen.txt` becomes extracted DOM text beside a `.png`.

---

## Forcing functions (without these it is theatre)

1. **Worker briefs carry a `## GATES` block.** Copy it from
   [`docs/SIXGATE.md`](../../docs/SIXGATE.md#the-gates-block-for-worker-briefs). G0 is
   authored and reviewed *before* implementation; the deliverable is `RESULTS.md`
   **plus** `docs/gates/<slug>/VERDICT.md` with all six artifacts committed. No
   VERDICT.md means the work is not reviewable, full stop.
2. **The merge bar includes `sixgate verdict --check docs/gates/<slug>`**, beside the
   dual review and green CI. It fails on a missing artifact, a failing gate, a stale
   source hash, or a G4 contract G2 never obeyed.
3. **Scaffolding order.** `sixgate scaffold` writes G0 and nothing else until it
   validates.

## Reference

- `docs/SIXGATE.md` — the contract, the `## GATES` block, the merge-bar line.
- `docs/gates/context-inspector/` — the first complete worked example: a real cold-eye
  report, an oracle that agrees to the token, and six honestly-unoracled figures whose
  on-screen estimate markers G2 asserts.
- `internal/sixgate/`, `tools/sixgate/` — the implementation. `sixgate selfcheck` is
  the gate on the gates.
