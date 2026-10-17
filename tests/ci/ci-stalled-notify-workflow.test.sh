#!/usr/bin/env bash
# Guards the event wiring of .github/workflows/ci-stalled-notify.yml. A fork
# run held for "Approve and run" is created already completed with conclusion
# action_required and emits only the `requested` workflow_run event, so a job
# gated on action == 'completed' never sees it (0 PRs labeled in 3363 runs).
# The stalled job must accept requested events carrying action_required, must
# only reconcile runs triggered by pull_request (never push or schedule), must
# use its own ci:awaiting-approval label, and every job must be time boxed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/ci-stalled-notify.yml"
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

# Body of one job: from its `  <name>:` line up to the next job header.
job_body() {
  awk -v job="  $1:" '
    $0 == job {f=1; print; next}
    f && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {exit}
    f {print}
  ' "$WORKFLOW"
}

# (1) Both workflow_run event types are subscribed to.
TYPES_LINE=$(grep -E '^[[:space:]]*types:' "$WORKFLOW" | head -n1 || true)
if grep -Fq 'requested' <<<"$TYPES_LINE" && grep -Fq 'completed' <<<"$TYPES_LINE"; then
  pass "workflow_run types include requested and completed"
else
  fail "workflow_run types must include requested and completed (got: ${TYPES_LINE:-none})"
fi

# (2) The stalled job fires on held runs and only for pull_request runs.
STALLED=$(job_body stalled)
# The job-level `if:` plus any folded continuation lines (indented deeper).
STALLED_IF=$(awk '
  /^    if:/ {f=1; print; next}
  f && /^      / {print; next}
  f {exit}
' <<<"$STALLED")
if [[ -z "$STALLED" ]]; then
  fail "stalled job not found"
fi

if grep -Fq "github.event.workflow_run.conclusion == 'action_required'" <<<"$STALLED_IF"; then
  pass "stalled job accepts runs whose conclusion is action_required (held runs emit only requested)"
else
  fail "stalled job if must accept github.event.workflow_run.conclusion == 'action_required'"
fi

if grep -Fq "github.event.workflow_run.event == 'pull_request'" <<<"$STALLED_IF"; then
  pass "stalled job only reconciles runs triggered by pull_request"
else
  fail "stalled job if must require github.event.workflow_run.event == 'pull_request'"
fi

if grep -Fq "github.event.action == 'completed'" <<<"$STALLED_IF"; then
  pass "stalled job still reconciles completed events (label removal)"
else
  fail "stalled job if must still accept github.event.action == 'completed'"
fi

# (3) Dedicated label, distinct from needs-ci (blocked on the author).
if grep -Eq '^[[:space:]]*STALL_LABEL:[[:space:]]*ci:awaiting-approval[[:space:]]*$' "$WORKFLOW"; then
  pass "STALL_LABEL is ci:awaiting-approval"
else
  fail "STALL_LABEL must be ci:awaiting-approval"
fi

# (4) Every job is time boxed.
for job in selftest stalled; do
  if grep -Eq '^[[:space:]]{4}timeout-minutes:[[:space:]]*[0-9]+' <<<"$(job_body "$job")"; then
    pass "$job job has timeout-minutes"
  else
    fail "$job job must set timeout-minutes"
  fi
done

if [[ "$ERRORS" -ne 0 ]]; then
  exit 1
fi
