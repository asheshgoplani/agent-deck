---
name: tdd
description: Red/green/refactor discipline for implementing any feature or bug fix — write the failing test first, watch it fail for the right reason, write the minimum code to pass, then refactor. Use before writing production code for a new behavior or a fix, and whenever tests are being added after the fact.
metadata:
  compatibility: "claude, opencode"
---

# TDD

## Iron law

> No production code without a failing test first. If production code was
> written before its test, **delete it** — do not keep it open as a
> reference while writing the test. A test written to match code you are
> looking at tests what the code does, not what it should do.

## The cycle

1. **RED — write the test.** One behavior, named for the behavior.

2. **VERIFY RED — run it and watch it fail.** Not "assume it fails." Two
   failure modes to distinguish, both required:
   - it must fail, and
   - it must fail **for the right reason** (the assertion, not a typo, a
     missing import, or a compile error).

   > A test that passes the first time you run it is testing behavior that
   > already exists. Either the behavior is already implemented — find out
   > why you thought otherwise — or the test does not exercise what you
   > think it does.

3. **GREEN — minimum code to pass.** Not the general solution. Not the next
   three cases. The minimum.

4. **VERIFY GREEN — run it again.** The target test passes, the rest of the
   suite still passes, and the output is **clean**: no new warnings, no new
   deprecation notices, no new log noise. Warnings introduced by your change
   are part of the change.

5. **REFACTOR — now, while it is green.** Check the result against
   `skills/review/references/principles.md`. Re-run the suite after.

## Gate 1 — before writing a test

Name, out loud, the **production change that would make this test fail**.
If you cannot name one, the test is not testing your change — redesign it
before writing it.

Worked example of a failed gate: a test asserts that `config.timeout ==
30`, but no code path reads `config.timeout` — the value is only ever
written, never consulted. The test would pass whether or not the timeout
logic exists, so it names nothing. Fix it by asserting on the observable
effect the timeout is supposed to produce (a request that aborts after 30
seconds), not the constant itself.

## Gate 2 — before adding a mock

List the real side effects the mock stands in for. Then:

> Never assert on the mock itself. `expect(mock.called).toBe(true)` asserts
> that you called your own test double; it says nothing about behavior.
> Assert on the observable result the side effect produces.

Prefer a real implementation, an in-memory fake, or a temp directory over a
mock whenever one is available — they exercise the real code path instead
of a stand-in you wrote yourself.

## Mutation check

After green, before moving on. Pick one and apply it mentally (or
actually, then revert):

- change a constant to a wrong value
- flip a branch condition
- delete a side effect (the write, the emit, the close)
- make a function return empty/zero

> If a mutation survives — nothing fails — that behavior is unprotected.
> Write the test that catches it.

## Exceptions require asking

Throwaway prototypes and generated code are the only two, and both need
the user to say so. "This is hard to test" is not an exception — it is a
design signal that the code needs a seam.

## Red flags

| Thought | Reality |
| --- | --- |
| "I'll add tests after" | Code without a test in front of it doesn't get one. |
| "The test is trivial, I know it fails" | Run it. "I know" is how untested behavior ships. |
| "I'll write all the tests, then all the code" | Batching breaks the fail-then-pass feedback loop; you lose per-test verification. |
| "I'll assert the mock was called" | That asserts you called your own double, not that behavior happened. |
| "The warning was already there" | Check. If your change didn't introduce it, say so — don't assume. |
