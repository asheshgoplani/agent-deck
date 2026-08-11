---
name: brainstorming
description: Collaborative design and brainstorming before any code is written — explores project context, asks one clarifying question at a time, offers 2–3 approaches with trade-offs, and writes an approved design document to its repository-local `.agent-deck/<date>-<slug>/design/` directory (git-ignored, never committed). Use before building a feature, adding functionality, or changing behavior, and whenever the user says "let's build", "I want to add", "how should we do X", or asks for a design or spec. Hard-gates implementation until the design is approved.
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
explicit approval covers all of those sections and the written document.

Do not ask for per-section approvals. Ask again only if later self-review
materially changes the approved scope: user-visible behavior, public
interfaces, data handling, or an explicitly excluded item. State the change
and why it needs confirmation; editorial fixes and clarifications do not need
another approval.

## 6. Write the spec

**Location: `.agent-deck/<date>-<slug>/design/design.md` in the root worktree,
git-ignored, never committed.** A design doc is scaffolding for the work, not a
deliverable — it must not land in a branch, a diff, or a PR. Its run root
groups the design with the later `plan/` and `orchestrate/` artifacts for the
same work. It goes in the repo's **main checkout** (not a worktree you may be
sitting in). Resolve the path and make the directory ignored *before* writing:

```bash
ROOT_WT=$(git worktree list --porcelain | awk '/^worktree /{print $2; exit}')
RUN_ID="YYYY-MM-DD-<topic>"
RUN_ROOT="$ROOT_WT/.agent-deck/$RUN_ID"
SPEC_PATH="$RUN_ROOT/design/design.md"
git -C "$ROOT_WT" check-ignore -q "$ROOT_WT/.agent-deck/.probe" || \
  printf '.agent-deck/\n' >> "$(git -C "$ROOT_WT" rev-parse --git-common-dir)/info/exclude"
git -C "$ROOT_WT" check-ignore -q "$ROOT_WT/.agent-deck/.probe"  # exit 0 before you write
mkdir -p "$(dirname "$SPEC_PATH")"
```

Probe with a path *inside* the directory, not the directory: asked about a
directory that has tracked files under it, `check-ignore` answers "not
ignored" even when the pattern matches, and you would add a duplicate rule and
still fail the gate. `.probe` need not exist.

`.git/info/exclude` is untracked and applies to every worktree of the repo, so
this costs the user no commit.

A repo whose convention is a *tracked* design-doc directory (`docs/plans/`,
`docs/rfcs/`) is the one case to raise: say you are writing to the ignored
run-local `design/` directory instead and why, and take the user's answer.

**Never `git add` the spec, and never verify it by looking for a commit.** Its
absolute path is what makes it findable — downstream sessions in other
worktrees read `$SPEC_PATH` directly, which works regardless of what any
branch contains. Verify that instead:

```bash
test -f "$SPEC_PATH" && git -C "$ROOT_WT" status --porcelain "$SPEC_PATH"
```

The file must exist and `status` must print nothing (ignored ⇒ invisible).

## 7. Spec self-review

Once written, read the document for placeholders (`TBD`, "etc.",
"handle errors"), internal contradictions, scope creep past what was
approved, and ambiguity a fresh reader would resolve differently than you
meant. Make non-material fixes in place. The prior design approval covers
that document; do not ask for a second document-review approval.

If self-review makes a material change to scope, user-visible behavior,
interfaces, data handling, or an explicitly excluded item, stop and obtain
one approval for that change before handing the spec on.

## 8. Tiered exit

After approval, size the work and take exactly one exit:

- **Orchestrated** — several independent tasks, non-obvious decomposition, a
  dedicated PR pipeline, or separate executor/reviewer sessions are needed →
  hand `$SPEC_PATH` (the absolute path, not the contents) to `orchestrate`. Do
  not write the plan yourself: orchestrate's planner child writes it against
  the codebase.
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
| "The spec dir is gitignored, I'll just keep it in the chat" | Chat isn't discoverable by the next session. Ignored is the point — write the file and hand on its absolute path. |
| "Downstream sessions need it committed to see it" | They read it by absolute path from the root worktree. Committing it only puts scaffolding in someone's PR. |
| "I'll ask all my questions at once to save time" | One merged answer covers two of five questions; the rest go unasked. |
| "They said build it, so approval is implied" | "Build it" approved the idea, not the design. Present the complete design and get one explicit sign-off. |
