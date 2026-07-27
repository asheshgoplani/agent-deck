---
name: debug
description: Systematic root-cause debugging for any bug, test failure, or unexpected behavior — investigate before fixing, compare against a working example, test one hypothesis at a time, and stop to question the architecture after three failed fixes. Use as soon as something breaks and before proposing or applying any fix.
metadata:
  compatibility: "claude, opencode"
---

# Debug

## Iron law

> No fixes without root-cause investigation first. A fix applied before you
> can state *why* the bug happens is a guess, and a guess that happens to
> make the symptom disappear is the expensive kind.

## Phase 1 — Investigate

1. **Read the actual error.** The whole message, the whole stack, the whole
   failing assertion — not the summary of it. Quote the line that failed.

2. **Reproduce it.** Find the smallest command that shows the failure
   reliably — a single test, a single request, a single CLI invocation.
   Note the reproduction rate: an intermittent bug and a deterministic one
   need different hunts, and a "fix" for an intermittent bug that you never
   actually reproduced is not verified, it's hoped.

3. **Check what changed recently.** `git log`, `git diff`, and the
   dependency lockfile. A bug that appeared today usually has a commit;
   `git bisect` earns its cost once the range of suspect commits is more
   than a handful.

4. **Instrument component boundaries.** Log or inspect the values crossing
   each boundary between the input and the symptom — function entry/exit,
   process/network boundary, read/write to shared state — and find the
   **first** boundary where the value is already wrong. That boundary, not
   the crash site, is where the bug lives. A `nil` dereferenced on line 200
   usually became `nil` many calls earlier.

## Phase 2 — Pattern analysis

Find a case that **works**: a sibling call site, an analogous handler, the
same function on different input, the same code before the breaking commit.
Diff the working case against the broken one and list every difference —
config, input shape, call order, environment. The cause is almost always in
that list, and it narrows the search space far faster than staring at the
broken path alone.

## Phase 3 — One hypothesis at a time

State the hypothesis as a falsifiable sentence ("X is nil at Y because Z
never runs when W"), not a vague hunch ("something's off with the config").
Design the **smallest** test that distinguishes it from the alternatives —
one that would come out differently depending on which hypothesis is true.
Change **one variable** per attempt: if you edit the retry logic and the
timeout in the same pass, a fix confirms nothing about either. Write down
the result before the next attempt — verbally "I tried a few things" is how
the same attempt gets made twice.

## Phase 4 — Fix, test-first

Write the failing test that reproduces the bug *before* the fix (chain out
to `tdd` for the red/green cycle), apply the fix, watch the test go green,
then run the full suite. A fix that only makes the reproduction case pass,
without a test locking in the behavior, will regress the next time someone
touches that code. Before claiming it is fixed, chain out to `verify`.

## 3-failed-fixes circuit breaker

> After the **third** failed fix attempt, stop. Do not attempt a fourth.
> Three failures means the model of the system is wrong, not that the fix
> was slightly off. Write down: what you believed, what you tried, what
> actually happened each time. Then question the architecture — is the
> component in the wrong place, is the invariant unenforceable, is this a
> symptom of a design problem — and take it to the human before attempt #4.

## Red flags

| Thought | Reality |
| --- | --- |
| "Let me just try changing this" | That's a guess, not a hypothesis. Name what you expect and why first. |
| "It's probably a race, let me add a sleep" | A sleep hides the race, it doesn't fix it. Find the actual ordering dependency. |
| "I'll add a null check and move on" | The null shouldn't be there. A guard just moves the symptom downstream. |
| "It works now, I don't know why" | Unexplained fixes regress. If you can't say why, you haven't found root cause. |
| "The test is flaky, let me retry it" | Flaky is a bug report. Retrying discards the evidence. |
| "Let me rewrite this function" | A rewrite without a diagnosed cause just relocates the bug. |

## Chains out to

- `tdd` — write the reproduction test before the fix, red/green/refactor.
- `verify` — confirm the fix before claiming the bug is resolved.
