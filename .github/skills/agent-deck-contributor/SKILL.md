---
name: agent-deck-contributor
description: Contribute to agent-deck (github.com/asheshgoplani/agent-deck) the way the maintainer's intake gate and review machine expect. Use when opening an issue or PR against agent-deck, fixing an agent-deck issue, responding to an intake or review comment on an agent-deck PR, or preparing a contribution on a human's behalf. Covers the full loop: claim, implement, self-verify, open, respond to verdicts.
metadata:
  compatibility: "any agent (Claude, Codex, Gemini, or human); no agent-deck-specific tooling required"
---

# agent-deck contributor

This skill is the contributor-side mirror of agent-deck's PR gate. The repo's intake
check (`.github/workflows/pr-intake.yml`), its field contract (`.github/INTAKE.md`),
and the maintainer's four-lens review machine are one spec read from the receiving
side; this skill is the same spec read from the sending side. An agent that follows
it passes intake on the first try and scores well on all four review lenses:
**correctness, security, fit, and intent**.

One principle before anything else: every field the gate requires is trivial for a
legitimate human+agent pair and impossible for intent-less slop. The one thing that
cannot be faked is a real human's ask. So the very first thing you capture, before
any code, is **what your human actually asked for, in their words**. Keep the quote.
It goes in the PR body verbatim.

## Script path resolution

This skill ships `scripts/self-check.sh`. Resolve it from the skill's own base
directory (shown when the skill loads), not the project root:

```bash
SKILL_DIR="/path/shown/in/base-directory-line"   # e.g. <repo>/.github/skills/agent-deck-contributor
"$SKILL_DIR/scripts/self-check.sh" pr-body.md
```

When working inside an agent-deck clone the path is
`.github/skills/agent-deck-contributor/scripts/self-check.sh`.

## Phase 1 — Claim and understand the issue

1. **Capture the human ask first.** Write down, verbatim, what your human asked for
   (one sentence is enough). If you are working from an issue with no direct human
   instruction, quote the issue author's problem statement instead. This becomes the
   `## What actually bothered you` section. Intake fails a PR whose intent section is
   empty; it never fails an honest one-liner.
2. **Reproduce before you touch code.** For bugs: build agent-deck, reproduce the
   reported behavior, and save the capture (terminal output, log lines). The review
   machine's intent lens asks "did the submitter supply real observed behavior, not
   just claims?" — your reproduction is that evidence. If you cannot reproduce
   reliably, say so honestly: "can't reproduce reliably, happens when ..." passes;
   silence does not.
3. **Comment on the issue to claim it** before starting, so the maintainer's fleet
   can tell you if someone (human or agent) is already on it.
4. **Check scope before committing effort:**
   - Features go to a Discussion first; feature PRs that arrive as surprises take
     longer, not shorter.
   - agent-deck holds a lean-scope line: no net-new tool adapters or bolt-on UIs
     without demonstrated demand. If your idea adds a new integration surface, open
     a Discussion and get a yes before writing it.
   - Anticipated diff over ~3000 added lines? Link an issue or Discussion agreeing
     the shape first, or the gate flags it `needs-discussion`.
   - You may have at most 5 open PRs on the repo at a time.
   - Security issues go through the private advisory route in `SECURITY.md`, never
     a public issue or PR.

## Phase 2 — Implement to the bar

1. **One problem per PR, scoped diff.** No unrelated churn, no drive-by refactors,
   no formatting sweeps of untouched files. The fit lens flags diffs over ~400
   changed lines as oversized; if your change is genuinely bigger, say in the PR
   body what could split out and why it didn't.
2. **Match the codebase style.** Read the surrounding package before writing. Go
   code: standard library first, existing helpers over new ones, table-driven tests
   like the neighbors have.
3. **Write a test that FAILS without your change.** This is the centerpiece of the
   correctness lens: the reviewer reverts your non-test hunks and re-runs your
   tests — a test that still passes proves nothing. Write the test first, watch it
   fail, then make it pass. `scripts/self-check.sh` automates this revert-check
   locally.
4. **Every changed hunk should be exercised by some test.** The correctness lens
   spot-checks changed-lines coverage and names untested hunks as flags.
5. **Run the exact CI sandbox invocation.** agent-deck tests must never run against
   a real home directory (they can destroy live user data), and CI runs them
   sandboxed. Use precisely:

   ```bash
   HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
   ```

6. **Format and vet before every push:**

   ```bash
   gofmt -w $(git diff --name-only origin/main -- '*.go')
   go vet ./...
   ```

   Lint CI treats formatting as a failure; the 2-second local pass saves a CI
   round-trip.
7. **Never touch `CHANGELOG.md`** — entries are added at landing time.
8. **Hot paths need timing evidence.** If you touch `list`, `status`,
   `session output`, startup, or the tmux layer, include simple before/after timing
   (even `time agent-deck list` runs) in the Evidence section.
9. **Avoid the tripwires** (details in `references/security-self-scan.md`):
   - New or bumped dependencies from a community PR are an automatic
     extra-scrutiny stop. If the fix truly needs a new module, justify it in the PR
     body (why this module, why it's canonical, where it's used) and expect a
     human gate. Never add a `replace` directive or a `GOPROXY` override.
   - Anything under `.github/` never auto-merges; it always waits for the
     maintainer personally. Keep workflow changes out of unrelated PRs.
   - No `curl | bash`, no downloaded-and-executed tooling, no new outbound network
     calls in production paths, no exec of shell-interpolated strings, no
     world-writable file modes, no `InsecureSkipVerify`.

## Phase 3 — Self-verify against the gate BEFORE opening

Draft your PR body into a file (start from `references/pr-body-template.md`), then
run the pre-open gate from the repo root:

```bash
.github/skills/agent-deck-contributor/scripts/self-check.sh pr-body.md
```

It mirrors, check for check, what the repo-side gate and review machine will do:
gofmt/vet/build, sandboxed tests, the revert-check, diff-size and forbidden-path
checks, the hidden-logic security sweep, and a lint of the PR body against the
intake contract (required headings, exactly one AI-disclosure box, model named when
AI helped, non-empty human intent, consistent gate marker).

**Fix every FAIL before opening.** WARNs are judgment calls: resolve them or
explain them in the PR body (a stated, honest imperfection passes review; a silent
one becomes a flag). The full check-by-check mapping, with the exact fix for each,
is in `references/gate-spec.md`.

## Phase 4 — Open the PR

1. Use `.github/PULL_REQUEST_TEMPLATE.md` verbatim — the intake Action parses the
   body by exact `## ` headings. Fill every required section:
   `What problem does this solve?`, `Why this change`, `User impact`, `Evidence`,
   `AI disclosure`, `What actually bothered you`, `Checklist`.
2. **AI disclosure:** check exactly one box, and if AI helped, name the model(s)
   (e.g. `claude-opus-4-x`, `gpt-5-codex`). `unsure` is a valid answer; blank is
   not. Disclosure is a quality signal that feeds a per-model track record — models
   that land clean earn lighter-touch review over time. It is never a filter.
3. **What actually bothered you:** paste the human ask you captured in Phase 1,
   quoted. This is the first thing a reviewer reads.
4. **Evidence, inline:** your reproduction capture, before/after behavior, the
   sandboxed test-suite result, and the revert-check result ("reverting the fix
   makes TestX fail: <one line of output>"). Real observed output, not claims;
   mock-only proof is insufficient for user-visible changes.
5. **End the body with the machine-readable marker** (last line, exact format):

   ```
   <!-- gate:ai=<human|assisted|authored> model=<name-or-unsure> intent=<yes|no> -->
   ```

   The visible sections are authoritative; the marker is a parse-stable
   confirmation that speeds routing. Make it agree with the checkboxes.
6. Check only the checklist boxes that are true. Leave "Allow edits from
   maintainers" enabled so urgent fixes can be taken over the line for you.
7. Do not mark anything `CRITICAL`/`URGENT`/`SECURITY` unless the body carries a
   concrete reproduction — urgency language plus scanner-style output with no repro
   is a named slop signal and gets deprioritized.

## Phase 5 — Respond to verdicts

Within about a day the PR gets a structured validation result. The loop from here:

1. **Intake comment (`needs-info`):** the bot names the exact missing field in
   machine-readable form. Edit the PR description — the check re-runs automatically
   and the label flips. No new commit needed for body-only fixes.
2. **Rank-up moves are the whole loop.** A `needs-work` verdict arrives with 1–3
   concrete actions that upgrade the verdict. Do exactly those, nothing else, then
   say so in a comment. needs-work is a loop with a re-review trigger, not a
   rejection.
3. **Verdicts are SHA-bound.** Every review verdict applies to one exact head SHA.
   Any push voids it and triggers re-review — so batch your fixes into one push
   rather than trickling commits, and never force-push over a reviewed SHA without
   saying what changed.
4. **Answer questions about the code.** Disclosed AI PRs are welcome on three
   conditions, and the third is that you (or your human) can defend the change when
   asked. The intent lens explicitly scores author understanding.
5. **If the gate misfires on your good-faith PR**, say so plainly in a comment and
   point at the evidence — the maintainer overrides bot mistakes by hand (there is
   a `bad-gate` label for exactly this) and the heuristic gets tuned. The gate is
   deliberately biased toward false-clean, so a wrong flag is rare and taken
   seriously.
6. **Silence is the only thing that auto-closes.** Any reply from you resets the
   clock. If life intervenes, one comment keeps the PR open; a closed PR can be
   reopened the moment you can add the missing piece.
7. **If your diff is declined but the problem is real**, the fleet may reimplement
   the intent with credit to you. A rejected diff is never a rejected idea — a
   well-evidenced problem statement is the most valuable thing you can leave
   behind.

## Hard rules (never do)

- Never open a PR with an empty or boilerplate `What actually bothered you`
  section. One real, quoted human sentence.
- Never claim AI-free authorship when a model wrote the code. Undisclosed
  bulk-generated PRs get closed; disclosed AI PRs get equal standing.
- Never run agent-deck tests against a real `$HOME` — always the sandbox
  invocation above.
- Never edit `CHANGELOG.md`; never fold `.github/` changes into an unrelated PR.
- Never add or bump a dependency without stating why in the PR body; never add
  `replace` directives or proxy overrides.
- Never introduce `curl | bash`, exec-of-constructed-strings, new outbound network
  calls in production paths, world-writable modes, weak crypto, or non-ASCII
  homoglyph/invisible characters in code.
- Never use urgency language (`CRITICAL`/`SECURITY`) without a concrete repro;
  real vulnerabilities go through `SECURITY.md` privately.
- Never exceed 5 open PRs; never open a >3000-added-line PR without a linked
  issue or Discussion.
- Never check a checklist box that is not true.
- Never expect a bot to merge: merges are always human, and a green gate is
  necessary but never sufficient.

## Traceability matrix

Every rule above traces to a named check on the maintainer's side. If the gate
changes, this skill changes with it — they are one spec.

| Skill rule | Gate-side source |
|---|---|
| Required `## ` headings, filled | `pr-intake.yml` check 1 (missing-headings) / `INTAKE.md` §PR body contract |
| Exactly one AI-disclosure box | `pr-intake.yml` check 2 (checked-boxes == 1) |
| Non-empty human intent, quote the ask | `pr-intake.yml` check 3 (meaningful intent) / gate design: human-provenance is the anti-slop asymmetry |
| Model named when AI helped; `unsure` valid | `INTAKE.md` marker contract; review-machine model provenance ledger (per-model track record) |
| Gate marker as last line, consistent | `INTAKE.md` §Machine-readable marker; `pr-intake.yml` marker parse |
| Test fails without the change (revert-check) | review machine, correctness lens: revert-check centerpiece |
| Changed hunks exercised by tests | review machine, correctness lens: diff-coverage spot-check |
| Exact sandbox test invocation | CONTRIBUTING §What makes a PR land fast; security gates: sandbox-everything rule (Gate 6) |
| gofmt + go vet before push | maintainer shipping rule (lint CI fails on formatting) |
| Scoped diff; ~400-line oversize flag | review machine, fit lens: oversized-pr flag |
| >3000 added lines needs linked discussion | intake gate: giant-undiscussed-diff check; `INTAKE.md` house rules |
| Lean-scope check before new adapters/UIs | review machine, fit lens: lean-scope line |
| Real repro/evidence, mock-only insufficient | review machine, intent lens: behavior-evidence test; `INTAKE.md` Evidence row |
| No urgency language without repro | intake gate: urgency-scanner-slop check |
| Dependency changes justified; no replace/GOPROXY | security gates, Gate 1 (dependency vet) + Gate 6 red flags |
| `.github/` changes always human-gated | security gates, Gate 2 |
| Hidden-logic self-scan (network/exec/homoglyph/perms/time-bomb/crypto) | security gates, Gate 3 sweep + Gate 6 red flags (author-side version: `references/security-self-scan.md`) |
| No CHANGELOG edits; ≤5 open PRs | `INTAKE.md` house rules; CONTRIBUTING house rules |
| Hot-path timing evidence | PR template checklist (hot-path row); CONTRIBUTING rule 6 |
| SHA-bound verdicts; push = re-review | review machine: SHA binding invariant |
| Rank-up loop on needs-work | review machine: rank-up doctrine (needs-work is a loop, not a wall) |
| Misfire → say so, human overrides | gate design: `bad-gate` override; false-clean bias |
| Silence-only close; reply resets clock | gate design: reaction ladder |
| Merges always human | branch protection + CODEOWNERS; every gate necessary-not-sufficient |

## References

- `references/gate-spec.md` — every intake check and review criterion, with the
  exact fix for each; the machine-readable companion to `.github/INTAKE.md`.
- `references/pr-body-template.md` — one complete example of a passing PR body,
  including the gate marker block.
- `references/security-self-scan.md` — the security sweeps rewritten as
  author-side greps you run on your own diff.
- Repo-side: `.github/INTAKE.md` (field contract), `CONTRIBUTING.md` (policy),
  `.github/PULL_REQUEST_TEMPLATE.md` (the body you fill).
