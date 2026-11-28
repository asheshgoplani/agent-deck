// ci-stalled-notify.cjs -- pure decision logic for .github/workflows/ci-stalled-notify.yml
//
// Kept outside the workflow so it can be unit tested with `node --test`
// (tests/ci/ci-stalled-notify.test.cjs) and required from github-script.
//
// Two reconciliation rules the review asked for:
//   1. A workflow_run event carries the head sha it ran for. If the PR has
//      moved on to a newer head, the event is stale: its own conclusion is
//      ignored and the decision comes from the live head's runs, so a delayed
//      completion for head A can neither clear nor invent a stall on head B.
//   2. Completion of one gating workflow must not clear the label while
//      another gating workflow on the same head is still action_required.
//      The caller passes every run for the current head; the label is
//      removed only when none of them is waiting for approval.

'use strict'

const STALLED = 'action_required'

/**
 * Decide what to do with the stall label for one PR.
 *
 * @param {object} input
 * @param {string} input.runHeadSha        head sha the workflow_run event ran for
 * @param {string} input.runConclusion     conclusion of that run ('action_required', 'success', ...)
 * @param {string} input.prHeadSha         live head sha of the PR
 * @param {boolean} input.labeled          whether the PR currently carries the stall label
 * @param {Array<{name:string, head_sha:string, conclusion:string|null}>} input.currentHeadRuns
 *        every run known for prHeadSha (any workflow); filtered here by gatingWorkflows
 * @param {string[]} input.gatingWorkflows workflow display names that count as gates
 * @returns {{action: 'add'|'remove'|'none', reason: string}}
 */
function reconcile({ runHeadSha, runConclusion, prHeadSha, labeled, currentHeadRuns, gatingWorkflows }) {
  if (!runHeadSha || !prHeadSha) {
    return { action: 'none', reason: 'missing head sha' }
  }

  // State-based: the decision depends on the live head's gating runs, not on
  // which event happened to trigger this job. The workflow queues events per
  // PR and a pending job can be replaced by a newer one, possibly an event for
  // an older head, so the job that survives must reach the decision for the
  // PR's live head. The trigger's own conclusion is trusted only when it ran
  // for that live head; a stale event is decided from the live listing alone.
  const fresh = runHeadSha === prHeadSha
  const gates = new Set(gatingWorkflows || [])
  const stillWaiting = (currentHeadRuns || []).filter(r =>
    r && r.head_sha === prHeadSha && gates.has(r.name) && r.conclusion === STALLED)
  if ((fresh && runConclusion === STALLED) || stillWaiting.length > 0) {
    if (labeled) return { action: 'none', reason: 'already labeled' }
    return { action: 'add', reason: 'a gating run on the current head is action_required' }
  }
  if (labeled) return { action: 'remove', reason: 'no gating run on the current head is action_required' }
  return { action: 'none', reason: 'not labeled and nothing waiting' }
}

/**
 * Whether a workflow_run event belongs to an open PR, for runs GitHub
 * attached no PR to (every fork PR). Matches when the PR's live head is the
 * run's sha, or when the run came from the PR's head repo and head branch:
 * the per-PR queue can hand the surviving job an event for a sha the PR has
 * since force-pushed away, and that job must still find the PR so it reaches
 * the live head's decision. Without a head repo on either side the branch
 * name alone is not trusted (another fork can reuse it), so only the sha
 * counts.
 *
 * @param {{head?: {sha?: string, ref?: string, repo?: {full_name?: string}|null}}} pr  from pulls.list
 * @param {{head_sha?: string, head_branch?: string, head_repository?: {full_name?: string}|null}} run  workflow_run payload
 * @returns {boolean}
 */
function matchesRun(pr, run) {
  const head = pr && pr.head
  if (!head || !run) return false
  if (head.sha && head.sha === run.head_sha) return true
  const prRepo = head.repo && head.repo.full_name
  const runRepo = run.head_repository && run.head_repository.full_name
  return Boolean(prRepo && runRepo && prRepo === runRepo &&
    head.ref && head.ref === run.head_branch)
}

function short(sha) {
  return String(sha).slice(0, 8)
}

module.exports = { reconcile, matchesRun, STALLED }
