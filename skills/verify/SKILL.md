---
name: verify
description: Evidence gate before any completion claim — every "it works", "tests pass", "fixed", or "done" must be backed by a command run in this message with its output shown. Use before committing, opening a PR, reporting a task complete, or telling the user something is working.
metadata:
  compatibility: "claude, opencode"
---

## Iron law

> No completion claim without fresh evidence **in this message**. Evidence
> from three messages ago is a memory, not a verification — the tree has
> changed since. Run the command now, show the output, then make the claim.

## Claim → evidence table

| Claim | Evidence required |
| --- | --- |
| "tests pass" | The suite run **now**, showing **0 failures**. A subset run proves the subset only — say which you ran. |
| "build works" | The build command exiting **0**. A passing linter is **not** a build; a type-check is not a build. |
| "the bug is fixed" | The **original symptom** re-tested by the original reproduction, now absent. |
| "I added a regression test" | A verified red-green cycle: revert the fix → the test **fails** → restore the fix → the test **passes**. Show both runs. |
| "the child/agent completed the work" | The **VCS diff** (`git log`, `git diff`) — never the agent's own success report. An agent reporting success is a claim, not evidence. |
| "nothing else broke" | The full suite, compared against a recorded baseline from before the change. |
| "it's deployed / running" | A request against the running thing, with its response. |
| "the type checker is happy" | The type-check command run now, exit 0 — distinct from tests and from a build. |
| "the migration is safe" | A run against a copy of real (or representative) data, not just the migration applying cleanly on an empty schema. |

## How to show evidence

Paste the command and the decisive lines of its output — the failure count,
the exit status, the assertion. Not the whole log. If the output is large,
show the tail and say what you filtered (e.g. "last 15 lines of 400; full log
had no other failures").

## Baselines

Before touching anything, record what already fails — run the suite, note
the failing names or count. Hold yourself accountable only for *new*
failures against that recorded baseline. A baseline claimed from memory
("I think that test was already broken") is not a baseline; if you didn't
run it before you changed anything, you don't have one, and every failure
is yours until proven otherwise.

## Red flags

| Red flag | Why it's a tell |
| --- | --- |
| "should work" / "probably passes" / "seems fine" | Hedging words mean no command was run — a command either passed or it didn't. |
| "Done!" / "Perfect!" / "All set!" before any output appears in the message | The claim is written before the evidence exists. |
| Citing a run from earlier in the conversation as current | The tree has changed since; stale evidence isn't evidence. |
| Reporting a subagent's or child session's summary as the result | Its success report is a claim, not a diff you've checked. |
| "The test file exists, so it's covered." | A file existing proves nothing about whether it runs or passes. |
| "It worked when I ran it manually earlier" | "Earlier" precedes the latest edit; re-run against the current tree. |
| Silence about a step that failed, followed by a claim about the step after it | A skipped failure doesn't disappear — it invalidates everything downstream. |

## When evidence cannot be gathered

Say so explicitly, and name both the reason and the gap — e.g. "no browser
available here, so the e2e path is unverified" or "no access to the
staging DB, so the migration is unverified against real data." An honest
unverified is fine; a claim dressed as verified is not.
