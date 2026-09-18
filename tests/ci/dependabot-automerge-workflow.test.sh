#!/usr/bin/env bash
# Guards issue #2131's Dependabot auto-merge workflow (PR #2141): auto-merge
# is only enabled for a Dependabot-authored PR, only for patch/minor bumps,
# never for a PR touching .github/, and the workflow never self-approves the
# PR it is merging.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/dependabot-automerge.yml"
ERRORS=0

fail() {
  echo "FAIL: $1" >&2
  ERRORS=$((ERRORS + 1))
}

pass() {
  echo "PASS: $1"
}

if [[ ! -f "$WORKFLOW" ]]; then
  fail "workflow file not found: $WORKFLOW"
  exit 1
fi

# (1) Actor gate: only dependabot[bot] can trigger auto-merge.
if grep -Fq "github.actor == 'dependabot[bot]'" "$WORKFLOW"; then
  pass "job is gated on github.actor == 'dependabot[bot]'"
else
  fail "job must be gated on github.actor == 'dependabot[bot]'"
fi

# (2) Update-type matching: only patch/minor are eligible, major falls through.
DECIDE_STEP=$(awk '/- name: Decide whether the bump qualifies/{f=1} f{print} /- name: Enable auto-merge/{exit}' "$WORKFLOW")

if grep -Fq 'version-update:semver-patch' <<<"$DECIDE_STEP" && grep -Fq 'version-update:semver-minor' <<<"$DECIDE_STEP"; then
  pass "eligibility matches semver-patch and semver-minor update types"
else
  fail "eligibility must match version-update:semver-patch and version-update:semver-minor"
fi

if grep -Fq 'version-update:semver-major' <<<"$DECIDE_STEP"; then
  fail "major bumps must not be explicitly allow-listed; they should fall through to the default case"
else
  pass "major bumps are not allow-listed (falls through to the default eligible=false case)"
fi

if grep -Eq '^\s*\*\)' <<<"$DECIDE_STEP" && grep -Fq 'eligible=false' <<<"$DECIDE_STEP"; then
  pass "unmatched update types (including major) default to eligible=false"
else
  fail "the case statement must have a default branch that sets eligible=false"
fi

# (3) .github/ path exclusion.
if grep -Eq "grep -q '\\^\\\\\\.github/'" <<<"$DECIDE_STEP"; then
  pass "changed-file check excludes PRs touching .github/"
else
  fail "must grep changed files for ^\\.github/ and mark ineligible on a match"
fi

if grep -A2 "grep -q '\\^\\\\\\.github/'" <<<"$DECIDE_STEP" | grep -Fq 'eligible=false'; then
  pass "a .github/ path match sets eligible=false"
else
  fail "matching .github/ in changed files must set eligible=false"
fi

# (4) No self-approval: the workflow enables auto-merge but never approves the PR.
if grep -Eq 'gh pr review .*--approve|pr\.createReview' "$WORKFLOW"; then
  fail "workflow must never approve the PR itself (required review must stay human)"
else
  pass "workflow never approves the PR (no gh pr review --approve / createReview)"
fi

if grep -Fq 'gh pr merge --auto --squash' "$WORKFLOW"; then
  pass "merge step uses --auto (deferred to required checks/review), not an immediate merge"
else
  fail "merge step must use 'gh pr merge --auto --squash', not an unconditional merge"
fi

if grep -Fq "steps.decide.outputs.eligible == 'true'" "$WORKFLOW"; then
  pass "the merge step is gated on the eligibility decision"
else
  fail "the merge step must be gated on steps.decide.outputs.eligible == 'true'"
fi

if [[ "$ERRORS" -ne 0 ]]; then
  exit 1
fi
