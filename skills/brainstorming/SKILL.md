---
name: brainstorming
description: Collaborative design and brainstorming before any code is written — explores project context, asks one clarifying question at a time, offers 2–3 approaches with trade-offs, and writes an approved, committed design document. Use before building a feature, adding functionality, or changing behavior, and whenever the user says "let's build", "I want to add", "how should we do X", or asks for a design or spec. Hard-gates implementation until the design is approved.
metadata:
  compatibility: "claude, opencode"
---

# Brainstorming

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

## 5. Present the complete design, approve once

Present motivation, decisions, architecture, interfaces, and out-of-scope
together in a skimmable document. Ask once: `Approve this design?` A single
explicit approval covers all of those sections and the committed document.

Do not ask for per-section approvals. Ask again only if later self-review
materially changes the approved scope: user-visible behavior, public
interfaces, data handling, or an explicitly excluded item. State the change
and why it needs confirmation; editorial fixes and clarifications do not need
another approval.

## 6. Write the spec

**Location:** honor the repo's visible convention — look for a directory
that already contains design docs (`docs/plans/`, `docs/specs/`,
`docs/design/`, `docs/rfcs/`) and use it. Only when none exists, default to
`docs/plans/YYYY-MM-DD-<topic>-design.md`. Before writing, check the exact
candidate path with `git check-ignore -q <spec-path>`: exit 0 means choose the
next conventional directory; exit 1 means the candidate is tracked. Never
write first and move the document later.

**Always committed, and verified committed.** A spec written into an
ignored directory looks committed and is invisible to every downstream
session. The check, verbatim:

```bash
git check-ignore -v <spec-path>          # must find nothing (exit 1)
git add <spec-path> && git commit -m "docs(plans): <topic> design"
git log -1 --oneline -- <spec-path>      # must print a commit
```

If `check-ignore` matches, choose a tracked directory before creating the
file — do not force-add it into an ignored tree.

## 7. Spec self-review and commit

Before committing, read the document for placeholders (`TBD`, "etc.",
"handle errors"), internal contradictions, scope creep past what was
approved, and ambiguity a fresh reader would resolve differently than you
meant. Make non-material fixes and commit. The prior design approval covers
that committed document; do not ask for a second document-review approval.

If self-review makes a material change to scope, user-visible behavior,
interfaces, data handling, or an explicitly excluded item, stop and obtain
one approval for that change before committing.

## 8. Tiered exit

After approval, size the work and take exactly one exit:

- **Orchestrated** — several independent tasks, non-obvious decomposition, a
  dedicated PR pipeline, or separate executor/reviewer sessions are needed →
  hand the committed design doc to `orchestrate`. Do not write the plan
  yourself: orchestrate's planner child writes it against the codebase.
- **Focused** — an obvious, low-risk change, even across a few closely related
  files → implement in-session under `tdd`, then `verify` before claiming
  done. Multi-file alone does not require orchestration.
- **Borderline →** ask **one** final question with your recommendation, and
  take the answer.

## Red flags

| Rationalization | Reality |
|---|---|
| "This is obviously what they want" | Confirm it anyway — the gate exists for the 20% of cases where it isn't. |
| "I'll design as I code" | That's implementation wearing a design costume. Stop and present first. |
| "The spec dir is gitignored, I'll just keep it in the chat" | Chat isn't discoverable by the next session. Move it to a tracked dir. |
| "I'll ask all my questions at once to save time" | One merged answer covers two of five questions; the rest go unasked. |
| "They said build it, so approval is implied" | "Build it" approved the idea, not the design. Present the complete design and get one explicit sign-off. |
