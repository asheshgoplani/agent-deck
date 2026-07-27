# Verification-gap review layer

## The single question

> If this changed behavior stopped holding where it is used, would any test fail?

That is the whole scope of this layer. It does not review quality, does not
review edge cases, and does not propose designs — it measures whether the
change is *protected*. Do not widen it.

## Inputs

You receive the diff, the full post-change content of every touched file, and
repo read access. Tracing consumers without repo access manufactures false
positives, so this layer is not run diff-only the way the adversarial layer
is.

## Step 1 — cheap triage first

Before tracing anything, check whether the whole diff falls into a change
class that carries no behavior to protect. If it does, emit the empty result
and stop — do not run Step 2 or Step 3 on it.

Whitelist (exit in one step, no findings): comment/doc-only changes; pure
formatting or import reordering; renames with no behavior change (call sites
updated mechanically); additive logging/metrics that nothing asserts on; new
dead code not yet wired to any caller; test-only changes (test files,
fixtures, mocks); generated files (lockfiles, generated bindings, vendored
output).

If any part of the diff falls outside the whitelist, proceed to Step 2 for
that part only; whitelisted hunks in the same diff still exit without
tracing.

## Step 2 — bounded consumer tracing

For each behavior the diff changes, walk outward from the changed symbol to
its callers. **1–3 hops maximum.** Stop the walk at whichever of these is hit
first, and record which one:

- A hop reaches a test file — record it: that is the protection.
- A hop reaches a public API, entry point, or handler boundary.
- A hop crosses into a third-party or vendored package.
- Hop 3 is reached, whatever it landed on.
- A fan-out wider than ~10 callers at any hop — record "wide fan-out,
  untraced" and stop without enumerating callers.

An untraced stop is a legitimate outcome — do not chase past these limits.

## Step 3 — the Demonstration gate

> Name the **one concrete mutation** you could make to the changed code that
> a consumer would observe and that no test would catch — a specific wrong
> constant, a specific flipped condition, a specific removed call. If you
> cannot name it concretely, **drop the finding.** A gap you cannot
> demonstrate is a guess.

Every candidate finding must pass this gate before it is reported — it is
what keeps this layer from reporting vague "insufficient test coverage".

**Passes:** the diff changes a retry backoff from exponential to fixed-delay
in `retryWithBackoff`. The only caller, `fetchWithRetry` (`client.go:88`),
has no test exercising retry timing — `client_test.go` only asserts the
happy path. Demonstration: hardcode the delay to `0` regardless of attempt
number; no test fails.

**Fails:** the diff renames an internal variable and reorders two
independent struct fields. There is no behavior change to mutate — the
"mutation" is just a restatement of the rename. Drop it; Step 1 should have
caught this already.

## Anti-fabrication clause

Never assert that a test exists, covers something, or passes unless you have
found the file and read the assertion. "There is probably a test for this" is
not a finding; "this is covered" without a `file:line` is a fabrication. Cite
`file:line` for every test you claim exists — a green suite is not a
substitute for reading the assertion; it says nothing about whether *this
specific* behavior is asserted.

## Output format

A numbered list:

```text
N. <file>:<line> — the behavior that changed, where it is consumed
   (file:line, or "untraced: <stop condition>"), and the demonstration
   mutation that nothing would catch.
```

Empty result is valid and expected for whitelisted changes:

```text
No verification gaps found.
```

## Severity ban

No severity, no ranking. Decided at merge, where the reviewer has the
context this layer was deliberately scoped without.
