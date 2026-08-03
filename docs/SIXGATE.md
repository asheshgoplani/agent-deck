# SIXGATE — artifact-first acceptance gating

> **"Done" is not a builder's claim, it is an artifact.**
> No transcript, not done.

A feature was declared done on the strength of code analysis, a 2,961-transcript corpus
replay and an adversarial review. Nobody ever pressed the key. The first thing its user
saw was a blank percentage they had to ask about.

Static verification cannot detect *"this feels broken on arrival"*. SIXGATE makes the
recording of the software being used the deliverable, alongside the code.

- **Skill:** [`skills/six-gates/SKILL.md`](../skills/six-gates/SKILL.md) — how to run it,
  the full safety block, and the honest split between what is universal and what is a
  swappable adapter.
- **Implementation:** `internal/sixgate/`, `tools/sixgate/`. `sixgate selfcheck` is the
  gate on the gates.
- **Worked example:** [`docs/gates/context-inspector/`](gates/context-inspector/) — the
  first feature to go through all six.
- **Sibling skill:** `capability-verification` audits the *product's* whole capability
  surface. SIXGATE gates *one feature* before it ships. Different questions; keep both.

## The six gates in one table

| Gate | Artifact | Passes when |
|---|---|---|
| **G0 SCRIPT** | `G0-script.yaml` | the journey exists as literal keystrokes, **written and reviewed before implementation** |
| **G1 DRIVE** | `NN-*.screen.txt`, `transcript.md`, `run.json` | it ran against the **real built artifact** and captured what a human would see; `pty_delta == 0` |
| **G2 ASSERT** | `results.{json,md}` | every assertion reads a **rendered frame**, the Blank Detector is clean, and G4's must-label contract is satisfied |
| **G3 MATRIX** | `matrix.{json,md}` | every declared world ran, including an un-excludable **cold first run** and a real-binary row |
| **G4 ORACLE** | `parity.{json,md}`, `must-label.json` | every figure agrees with a truth the author did not write, or drifts with a written reason, or **has no oracle and the screen says so** |
| **G5 COLD-EYE** | `brief.md`, `report.md`, `resolutions.yaml`, `outcome.json` | a reviewer given **only** the binary and one sentence reported back, uncontaminated, and every "looked broken" item is fixed or accepted in writing |
| **VERDICT** | `VERDICT.{md,json}` | all six pass **and** every source the feature declares still hashes to what the evidence covered |

Two mechanisms carry most of the weight and transfer to any project:

1. **The Blank Detector** — a no-opt-in lint on every frame in every gate for `( %)`,
   `()`, `NaN`, `<nil>`, `%!d(MISSING)`, `undefined`, `null`, `[object Object]`,
   `{{ .Name }}`, a bare `--` where a number belongs, a label with nothing after it.
   It catches "feels broken on arrival" *without anyone knowing to look for it*.
2. **Unoracled ⇒ must be labelled** — G4 emits the figures nobody has a truth source
   for; G2 asserts each is marked an estimate **on the recorded screen**; the verdict
   compares the digest G2 obeyed against the file G4 wrote. A number with no oracle and
   no label is a gate failure, not a footnote.

---

## G4b — TESTIMONY: the conversational oracle

G4's panel oracles verify **counts**, and they have a reach problem: `/context` exists
only on Claude Code, and `/status` on Codex prints totals with no names in them. But
every conversational harness ships one oracle for free — **the session's own agent**.
Its context is literally in front of it, so it can be asked for *checkable strings*:
"quote the exact first line of your project CLAUDE.md", "is skill X fully loaded or
only its name and description?", "name the memory files you can see". Testimony is the
only cross-harness oracle for **identity**, and identity is all it is trusted with.

**The division of labour** (three independent layers, none substitutable for another):

| layer | verifies | mechanism |
|---|---|---|
| **G4b testimony** | **IDENTITY** — which files, skills, text are in the window | ask the probe's agent for quotes; grade the inspector against them |
| **G4 panel oracles** | **COUNTS** — how many tokens each costs | numbers somebody else's software produced |
| **`--strict`** | **SELF-CONSISTENCY** — the report against its own sums | exit 3 on a report that contradicts itself |

Token counts are **out of scope for G4b by design**, and every artifact says so in its
header: an agent cannot count its own tokens, so grading a count against testimony
would launder an opinion into a measurement.

**The probe protocol** (`sixgate probe <slug> --yes`):

1. **Plant a known recipe** in a fresh `/tmp` directory: a project CLAUDE.md whose
   first line is a nonce-carrying beacon, and one skill whose description carries the
   same nonce. Every question resolves to a string the recipe planted, so the right
   answer is known before the probe is asked.
2. **Launch a disposable probe session** there via the agent-deck CLI.
3. **Converse** — 3–5 quote-based questions, single-line sends, `--wait -q`. Demand
   quotes and single committed words, never opinions; a quote is a checkable string,
   an opinion is not evidence.
4. **Inspect** — run `session context <probe> --json` (and `--item` for the beacon
   file's L3 text, with the item id read out of the inspector's own document).
5. **Compare** — the `internal/sixgate/testimony` comparator (standard library only;
   it cannot import the product) grades four fixed claims: the beacon line's verbatim
   text against the inspector's `--item` text, the loaded memory files against the
   paths the probe names, loaded-versus-listed for the planted skill, and the skill
   description quote against the inspector's catalogue text.
6. **Tear down ALWAYS** — stop + remove on every exit path, then **prove** the removal
   against `agent-deck list` and record all three bits in the artifact. "We asked for
   removal" and "it is gone" are different claims, and only the second is evidence.

The artifacts land in `docs/gates/<slug>/G4b-testimony/`: `testimony.json` and
`testimony.md` (the agreement table and verdict) plus `transcript.md` (the questions
and replies, verbatim). G4b is a **supplement to G4, not a seventh gate** — the gate
catalogue stays at six and `verdict --check` does not require it.

**The consent rule, absolute.** The probe verb converses only with a session it
created itself — there is deliberately no flag to aim it at an existing session. It
refuses without `--yes` (a billed session is launched; the pre-flight output says so
first) and refuses inside a matrix row, so G3's unattended runner can never reach it.
Grading an **existing** session by testimony is legitimate only with a human's
explicit per-run consent, in the sanctioned manual form: the human asks the questions
in their own terminal and compares by hand. No automation in this repository types
into a session it did not create.

**The limits, honestly.** Testimony is not measurement — never grade a token figure
against it. Agents summarize, paraphrase and forget — which is why every question
demands a quote, and why the comparator grades a hedged answer (`LISTED`, "though
arguably `LOADED`") as *unverifiable*, never as agreement. And a disagreement is a
**finding to investigate, not automatically an inspector bug**: the agent may see a
file under another spelling, or may have paraphrased. The rows say "disagree"; they do
not say who is wrong.

---

## The `## GATES` block for worker briefs

Every worker brief for a feature or a user-visible fix carries this block verbatim.
A brief without it produces work that cannot be reviewed.

```markdown
## GATES — SIXGATE applies to this work

"Done" is not your claim, it is an artifact. Read `skills/six-gates/SKILL.md` first.

1. **BEFORE writing any implementation code**, author `docs/gates/<slug>/G0-script.yaml`:
   the user journey as literal keystrokes, in the words of whoever asked for this, with
   at least one assertion per captured frame. Post it for review and WAIT. If you cannot
   write the journey down, the feature is not defined yet — say so instead of guessing.
   `sixgate scaffold <slug>` refuses to create G1..G5 until G0 validates.
2. Write the assertion that would have FAILED the original bug. Assert a number **or**
   an honest sentence — never merely that something is present, because a blank
   percentage is present too.
3. Run G1..G5. Expect the first real drive to CORRECT your script: fix the script, never
   relax an assertion until it passes. Record what the frames taught you that source
   reading had not.
4. G4: every number the feature shows must be compared against a source of truth you did
   not write, or be labelled an estimate on screen — and G2 must prove the label renders.
5. G5: a reviewer who has never seen this code gets the binary and one sentence. Answer
   every item they call broken with `fixed` or `accepted` plus a real reason.
6. **DELIVERABLE:** `$WORKDIR/RESULTS.md` **and** `docs/gates/<slug>/VERDICT.md` with all
   six artifacts committed, and `sixgate verdict <slug> --check` exiting 0. No VERDICT,
   no review.

SAFETY: sandbox every run (`HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME=
XDG_CACHE_HOME= CLAUDE_CONFIG_DIR=`); never `go test` against a real home; never write to
`~/.agent-deck`; any tmux goes on a dedicated `-L` socket whose teardown you PROVE
(`tmux -L <name> ls` must fail) with a PTY census before and after; identify tmux by
socket path only, never by name or argv.
```

## The merge bar

A change lands when **all** of these hold:

1. Claude review clean **and** Codex review clean.
2. CI green.
3. For any feature or user-visible fix: `sixgate verdict <slug> --check` exits 0.

Point 3 is not a duplicate of point 2. CI proves the code does what its tests say; the
verdict proves somebody pressed the key, saw what a user would see, checked the numbers
against something the author did not write, and had a stranger try it. It also expires
on its own: the check re-hashes every source the feature declares in `owns`, so a
transcript recorded three commits ago stops counting as evidence.

```sh
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= \
  ./build/sixgate verdict <slug> --check
```

Failure modes it reports, each of which has happened to somebody:

- a gate whose artifacts are missing entirely (**no transcript, not done**);
- a gate present but failing — presence and passing are tracked separately on purpose,
  because conflating them is how "we ran it" becomes "it passed";
- a hand-edited `VERDICT.md` (the check re-renders it from the JSON and byte-compares);
- a source file that changed, appeared or disappeared since the evidence was recorded;
- a G4 must-label contract that G2 never obeyed, or obeyed in an older, weaker version.
