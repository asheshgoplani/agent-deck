---
name: design
description: Collaborative design and brainstorming before any code is written — explores project context, asks one clarifying question at a time, offers 2–3 approaches with trade-offs, and writes an approved, committed design document. Use before building a feature, adding functionality, or changing behavior, and whenever the user says "let's build", "I want to add", "how should we do X", or asks for a design or spec. Hard-gates implementation until the design is approved.
metadata:
  compatibility: "claude, opencode"
---

# Design

**If this session was dispatched as an executor, stop reading here.** Two
tells: the session prompt says the work is already designed and approved,
or `tmux show-environment AGENTDECK_ROLE` prints `AGENTDECK_ROLE=child`.
(Check tmux, not `env` — the marker lives in the tmux *session*
environment, which a process that was already running does not inherit.)
An executor does not brainstorm: its task prompt is the contract. Follow
the task prompt. Do not write a spec, do not propose alternatives, do not
wait for an approval that no one in this session can give. If you believe
the task is genuinely wrong, say so in one line and stop.

## The hard gate

No implementation — and no implementation skill — until the design has
been presented and the user has approved it. This holds regardless of how
simple the change looks. "It's a one-liner" is the most common way this
gate gets skipped, and a one-liner with the wrong requirement is still the
wrong one-liner.

## 1. Explore project context first

Before the first question: read the repo's `README`, `CLAUDE.md`/`AGENTS.md`,
and `CONTRIBUTING.md`; find the existing design docs directory; look at how
the nearest analogous feature is built. State what you found in a few lines.

Questions asked without context waste the user's turns on things the repo
already answers.

## 2. Clarifying questions — one per message

Ask the highest-leverage unknown, wait for the answer, then ask the next.
A batch of five questions gets one merged answer that addresses two of
them — the rest silently go unanswered. Stop asking once the remaining
unknowns no longer change the shape of the design.

Usually worth asking:

- Who uses this, and how?
- What breaks today that this needs to fix?
- What must not change (behavior, interfaces, data)?
- How is this expected to be verified — tests, manual check, both?

## 3. Approaches — 2–3, with trade-offs and a recommendation

Each approach gets: what it does, what it costs, what it forecloses. Then a
named recommendation with a one-line reason.

**YAGNI ruthlessly**: cut anything that serves a requirement the user did
not state, and say what you cut.

## 4. Principles pass

Before presenting the chosen architecture, check it against
`${CLAUDE_PLUGIN_ROOT}/skills/review/references/principles.md` (that path is
relative to the installed plugin, not to the repo you are working in). The
question to answer out loud:
*does any component here exist for a requirement nobody stated?* Cut or
justify each one.

## 5. Present in sections, approve after each

Motivation → decisions → architecture → interfaces → out-of-scope. Stop
after each section for approval rather than delivering the whole thing at
once — a wrong premise caught in section one saves rewriting sections two
through five.

## 6. Write the spec

**Location:** honor the repo's visible convention — look for a directory
that already contains design docs (`docs/plans/`, `docs/specs/`,
`docs/design/`, `docs/rfcs/`) and use it. Only when none exists, default to
`docs/plans/YYYY-MM-DD-<topic>-design.md`.

**Always committed, and verified committed.** A spec written into an
ignored directory looks committed and is invisible to every downstream
session. The check, verbatim:

```bash
git check-ignore -v <spec-path>          # must find nothing (exit 1)
git add <spec-path> && git commit -m "docs(plans): <topic> design"
git log -1 --oneline -- <spec-path>      # must print a commit
```

If `check-ignore` matches, move the file to a tracked directory — do not
force-add it into an ignored tree.

## 7. Spec self-review, then the user review gate

Before handing it over, read your own document for: placeholders (`TBD`,
"etc.", "handle errors"), internal contradictions, scope creep past what
was approved, and ambiguity a fresh reader would resolve differently than
you meant. Fix what you find, then explicitly ask the user to review the
written file — approval of the conversation is not approval of the
document.

## 8. Tiered exit

After approval, size the work and take exactly one exit:

- **Multi-task / multi-file / PR-worthy →** hand the committed design doc
  path to the `orchestrate` skill. Do not write the plan yourself:
  orchestrate's planner child writes it against the codebase.
- **Genuinely tiny** — one file, one sitting, no new interfaces →
  implement in-session under `tdd`, then `verify` before claiming done.
- **Borderline →** ask **one** final question with your recommendation, and
  take the answer.

## Red flags

| Rationalization | Reality |
|---|---|
| "This is obviously what they want" | Confirm it anyway — the gate exists for the 20% of cases where it isn't. |
| "I'll design as I code" | That's implementation wearing a design costume. Stop and present first. |
| "The spec dir is gitignored, I'll just keep it in the chat" | Chat isn't discoverable by the next session. Move it to a tracked dir. |
| "I'll ask all my questions at once to save time" | One merged answer covers two of five questions; the rest go unasked. |
| "They said build it, so approval is implied" | "Build it" approved the idea, not the design. Present sections and get explicit sign-off. |
