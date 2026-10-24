# Contributing to agent-deck

> Part of the agent-deck skill. Read this when working on the agent-deck repository
> itself: fixing, adding or reviewing something in its code or docs, opening a PR, or
> answering an intake or review verdict on one. It is the workflow only.
> [AGENTS.md](../../../AGENTS.md) at the repo root is the single source of facts
> (commands, CI, conventions, house rules); this page links to it instead of repeating it.

The repo's intake check (`.github/workflows/pr-intake.yml`), its field contract
([.github/INTAKE.md](../../../.github/INTAKE.md)) and the maintainer's four-lens review
(correctness, security, fit, intent) are one spec read from the receiving side. This
workflow is the same spec read from the sending side. The check-by-check mapping, with
the exact fix for each failure, is [contributing-gate-spec.md](contributing-gate-spec.md).

## Scripts

`self-check.sh` ships in this skill at `scripts/contributing/self-check.sh`. In an
agent-deck checkout run it from the repo root as
`skills/agent-deck/scripts/contributing/self-check.sh`; elsewhere resolve it from the
skill's base directory (`$SKILL_DIR/scripts/contributing/self-check.sh`). It runs on the
current checkout wherever it lives, since it finds the repo root with
`git rev-parse --show-toplevel`.

## 1. Capture the human ask

Before any code, write down verbatim what your human asked for (one sentence is
enough). Working from an issue with no direct instruction, quote the issue author's
problem statement. It becomes `## What actually bothered you`, the one field intake
never accepts blank and the anti-slop signal nothing else can fake.

## 2. Claim or discuss

Follow [AGENTS.md: Before you start](../../../AGENTS.md#before-you-start-claim-or-discuss)
and [Do not touch without asking first](../../../AGENTS.md#do-not-touch-without-asking-first-in-the-issue-and-wait-for-a-yes-before-the-pr).
On top of that:

- Check scope before spending effort. agent-deck holds a lean-scope line: no net-new
  tool adapters or bolt-on UIs without demonstrated demand. A new integration surface
  needs a Discussion and a yes before you build it.
- Expecting over ~3000 added lines? Agree the shape in an issue or Discussion first;
  `self-check.sh` fails such a diff unless `LINKED_ISSUE=<n>` is set.
- Found the problem in your own usage? Run [retro.md](retro.md) locally, reproduce each
  candidate with [repro.md](repro.md), then show your human the exact privacy-reviewed
  draft and let them file it. Never file it on their behalf. Keep the issue number they
  get back for the PR. For an existing issue, start at [repro.md](repro.md).

## 3. Branch and reproduce

Branch from `main` as `fix/...`, `feature/...` or `docs/...` (see
[AGENTS.md: Pull request](../../../AGENTS.md#pull-request)). For a bug, reproduce before
touching production code, following [repro.md](repro.md): the real affected binary in a
container or throwaway environment, with the command, revision, fixture, output and exit
status kept. Say `reproduced` only when the reported symptom is observed. A failed build
or missing dependency is not a reproduction; report `not reproduced` with the attempt.

## 4. Write the failing test first

Turn the reproduction into a regression test and watch it fail on the unfixed code, then
make the smallest fix (the rule is in [AGENTS.md: Conventions](../../../AGENTS.md#conventions)).
Say `fixed` only when the same reproduction passes on the fixed build and the test fails
on the affected build and passes on the fixed one. Keep all four receipts and the
`result.json` from [repro-contract.md](repro-contract.md).

While implementing:

- One problem per PR, no drive-by churn. The fit lens flags diffs over ~400 changed lines;
  if yours is bigger, say in the body what could split out and why it didn't.
- Every changed hunk should be exercised by some test; the correctness lens names
  untested hunks. If a hunk is untestable, say why in the body.
- Read the neighbouring code first and match it.
- Avoid the security tripwires listed in
  [contributing-security-self-scan.md](contributing-security-self-scan.md) and run its
  greps on your own diff.

## 5. Check your diff against the four review rules

Before running anything, reread your diff against
[AGENTS.md: Review rules](../../../AGENTS.md#review-rules-bug-classes-that-keep-coming-back):
fix the siblings, resolve the profile first, no byte cuts on display text, join what you
start. These bug classes keep coming back and review checks for each one.

## 6. Run the checks the way CI does

Never run `go test` on your host. The container rule and the commands are in
[AGENTS.md: Safety rule](../../../AGENTS.md#safety-rule-tests-never-touch-a-real-home-or-your-tmux-server)
and [Build and check exactly as CI does](../../../AGENTS.md#build-and-check-exactly-as-ci-does).
With a PR body draft in a file (step 7), the pre-open loop is:

1. Run `self-check.sh pr-body.md` on the host (add `FULL_TESTS=1` for every package,
   `REPRO_REPORT=<result.json>` for a bug fix). It runs gofmt, vet and build, reports the
   sandboxed tests and the revert-check as WARN, prints the `docker run --rm ...` command
   to rerun it in a container, then the summary ending `Not ready yet: go test has not run`.
2. Run that command from the same checkout. It mounts the source and the body read-only,
   copies them to `/tmp/src`, sets `AGENTDECK_TEST_CONTAINER=1` with a throwaway `HOME`
   and `XDG_*` dirs, installs tmux if the image lacks it, and reruns the check there.
   `SELF_CHECK_IMAGE` overrides the default `golang:<go.mod version>` image.
3. Open only when that run ends with `Ready to open`. Already inside a disposable
   container? Run `self-check.sh` there directly; it detects the container.

Fix every FAIL. Resolve each WARN or explain it in the PR body: a stated imperfection
passes review, a silent one becomes a flag. A `REPRO_REPORT` must carry the `fixed`
verdict; the validator checks the manifest and its local artifacts, and never runs the
commands in it.

## 7. Write PR text that passes the intake bot

The required headings, disclosure, intent, checklist and marker rules are in
[AGENTS.md: Pull request](../../../AGENTS.md#pull-request); a complete passing body is
[contributing-pr-body-template.md](contributing-pr-body-template.md). What trips agents:

- Copy the template headings exactly. Intake matches a `#` to `###` heading at line start,
  case-insensitively with punctuation and emoji ignored, skips headings inside code
  fences, and counts a section with nothing under it as missing. `self-check.sh` is
  stricter (`^## Heading`), so exact `## ` headings pass both.
- `## What actually bothered you`: paste the ask from step 1, quoted.
- `## Evidence`, inline: the repro verdict and receipts for a bug fix, before/after
  behaviour, the in-container test result, and the revert-check result ("reverting the
  fix makes TestX fail: <one line>"). Mock-only proof is not enough for user-visible
  changes.
- No `CRITICAL`/`URGENT`/`SECURITY` wording without a concrete reproduction; with
  scanner-style output and no repro it is a named slop signal. Real vulnerabilities go
  through `SECURITY.md` privately.
- The gate marker is the very last line and agrees with the checked box.

## 8. Respond to intake and review verdicts

- **Intake `needs-info`:** the bot comment names the missing field. Edit the PR
  description; the check reruns on edit and no commit is needed. The intake check only
  labels and comments: it reports green either way and never closes anything.
- **Review `needs-work`:** it arrives with 1-3 rank-up moves. Do exactly those, nothing
  else, and say so in a comment; the push triggers re-review.
- **Verdicts are SHA-bound:** any push voids the verdict on the old head. Batch fixes into
  one push, and never force-push over a reviewed SHA without saying what changed.
- **Questions about the code:** answer them yourself, substantively. The intent lens scores
  whether the author understands the change.
- **A misfire on a good-faith PR:** say so in a comment with the evidence. The maintainer
  fixes labels by hand and may add `bad-gate`, after which intake leaves labels and comment
  alone.
- **Silence:** nothing auto-closes. After 10 days without activity on a `needs-info` PR,
  `needs-info-nudge.yml` posts one reminder; a `keep-open` label suppresses it.
- **Merge** is always by the human maintainer, after clean reviews and green CI. A green
  gate is necessary, never sufficient. A declined diff with a real, well-evidenced problem
  may be reimplemented with credit to you.

## Never

- Claim AI-free authorship when a model wrote the code, or leave the model unnamed
  (`unsure` is a valid answer).
- Check a checklist box that is not true.
- Run agent-deck tests against a real `$HOME` or on your host.
- Edit `CHANGELOG.md`, or fold `.github/` changes into an unrelated PR.
