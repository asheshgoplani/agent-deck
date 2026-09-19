// ci-stalled-notify.cjs -- pure decision logic for .github/workflows/ci-stalled-notify.yml
//
// Kept outside the workflow so it can be unit tested with `node --test`
// (tests/ci/ci-stalled-notify.test.cjs) and required from github-script.
//
// Two reconciliation rules the review asked for:
//   1. A workflow_run event carries the head sha it ran for. If the PR has
//      moved on to a newer head, the event is stale and must not touch the
//      label, otherwise a delayed completion for head A clears a stall that
//      is real on head B.
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
  if (runHeadSha !== prHeadSha) {
    return { action: 'none', reason: `stale event: run head ${short(runHeadSha)} != PR head ${short(prHeadSha)}` }
  }

  if (runConclusion === STALLED) {
    if (labeled) return { action: 'none', reason: 'already labeled' }
    return { action: 'add', reason: 'a gating run on the current head is action_required' }
  }

  const gates = new Set(gatingWorkflows || [])
  const stillWaiting = (currentHeadRuns || []).filter(r =>
    r && r.head_sha === prHeadSha && gates.has(r.name) && r.conclusion === STALLED)
  if (stillWaiting.length > 0) {
    const names = [...new Set(stillWaiting.map(r => r.name))].join(', ')
    return { action: 'none', reason: `other gating run(s) still action_required on this head: ${names}` }
  }
  if (labeled) return { action: 'remove', reason: 'no gating run on the current head is action_required' }
  return { action: 'none', reason: 'not labeled and nothing waiting' }
}

function short(sha) {
  return String(sha).slice(0, 8)
}

module.exports = { reconcile, STALLED }
