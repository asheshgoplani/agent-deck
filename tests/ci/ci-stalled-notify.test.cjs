// tests/ci/ci-stalled-notify.test.cjs -- unit tests for the stall-label
// reconciliation used by .github/workflows/ci-stalled-notify.yml.
// Run: node --test tests/ci/ci-stalled-notify.test.cjs

'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const { reconcile, matchesRun } = require('../../.github/scripts/ci-stalled-notify.cjs')

const GATES = ['Go tests', 'golangci-lint', 'CodeQL']
const A = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const B = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

function run(name, head_sha, conclusion) {
  return { name, head_sha, conclusion }
}

test('stalled run on the current head adds the label', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: A, labeled: false,
    currentHeadRuns: [run('Go tests', A, 'action_required')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'add')
})

test('held run seen on its requested event (already action_required) adds the label', () => {
  // A held fork run is created completed/action_required and emits only the
  // `requested` workflow_run event; the listed runs carry no head_sha here.
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: A, labeled: false,
    currentHeadRuns: [{ name: 'Go tests', conclusion: 'action_required' }], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'add')
})

test('stalled run on an already labeled PR does nothing', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: A, labeled: true,
    currentHeadRuns: [], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /already labeled/)
})

test('stale completion for an old head never clears a stall on the new head', () => {
  // Head A finished late; the PR has moved to head B, which is stalled.
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: B, labeled: true,
    currentHeadRuns: [run('Go tests', B, 'action_required')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /already labeled/)
})

test('stale event reconciles the live head: held gate on the new head is labeled', () => {
  // A's success event replaced B's pending job; B is held and unlabeled.
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: B, labeled: false,
    currentHeadRuns: [run('Go tests', B, 'action_required')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'add')
})

test('stale event reconciles the live head: cleared new head loses the label', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: B, labeled: true,
    currentHeadRuns: [run('Go tests', B, 'success')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'remove')
})

test('a stale action_required conclusion is not trusted; the live head decides', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: B, labeled: false,
    currentHeadRuns: [run('Go tests', B, 'success')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /nothing waiting/)
})

test('one gate completing keeps the label while another gate on the same head still waits', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: true,
    currentHeadRuns: [
      run('golangci-lint', A, 'success'),
      run('Go tests', A, 'action_required'),
      run('CodeQL', A, 'success'),
    ],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /already labeled/)
})

test('label is removed once every gate on the current head has left action_required', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: true,
    currentHeadRuns: [
      run('golangci-lint', A, 'success'),
      run('Go tests', A, 'failure'),
      run('CodeQL', A, 'success'),
    ],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'remove')
})

test('non-gating workflows waiting for approval do not hold the label', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: true,
    currentHeadRuns: [
      run('Go tests', A, 'success'),
      run('Structure advisory', A, 'action_required'),
    ],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'remove')
})

test('runs recorded for another head are ignored when aggregating', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: true,
    currentHeadRuns: [run('Go tests', B, 'action_required'), run('Go tests', A, 'success')],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'remove')
})

test('completion on an unlabeled PR with nothing waiting is a no-op', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: false,
    currentHeadRuns: [], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
})

test('missing shas never mutate labels', () => {
  assert.equal(reconcile({ runHeadSha: '', runConclusion: 'action_required', prHeadSha: A, labeled: false, currentHeadRuns: [], gatingWorkflows: GATES }).action, 'none')
  assert.equal(reconcile({ runHeadSha: A, runConclusion: 'success', prHeadSha: undefined, labeled: true, currentHeadRuns: [], gatingWorkflows: GATES }).action, 'none')
})

test('unlabeled PR with a held gate is labeled even when the triggering run is a success (F2)', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: false,
    currentHeadRuns: [
      { name: GATES[0], head_sha: A, conclusion: 'success' },
      { name: GATES[1], head_sha: A, conclusion: 'action_required' },
    ],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'add')
})

test('held gate on an already labeled PR stays a no-op when triggered by a success', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: A, labeled: true,
    currentHeadRuns: [{ name: GATES[1], head_sha: A, conclusion: 'action_required' }],
    gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
})

// matchesRun(pr, run): which open PR a workflow_run event belongs to when
// GitHub attached none (always the case for fork PRs). The per-PR queue can
// hand the surviving job an event for a head the PR has force-pushed away, so
// the lookup must also match by head repo + head branch, not by sha alone.
const FORK = 'someone/agent-deck'
const OTHER_FORK = 'other/agent-deck'

function pr(sha, repoFullName, ref) {
  return { head: { sha, ref, repo: repoFullName ? { full_name: repoFullName } : null } }
}
function wfRun(sha, repoFullName, branch) {
  return { head_sha: sha, head_branch: branch, head_repository: repoFullName ? { full_name: repoFullName } : null }
}

test('matchesRun: PR whose live head is the run sha matches', () => {
  assert.equal(matchesRun(pr(A, FORK, 'fix'), wfRun(A, FORK, 'fix')), true)
})

test('matchesRun: event for a force-pushed-away sha on the same fork repo + branch matches', () => {
  assert.equal(matchesRun(pr(B, FORK, 'fix'), wfRun(A, FORK, 'fix')), true)
})

test('matchesRun: another fork with the same branch name does not match', () => {
  assert.equal(matchesRun(pr(B, OTHER_FORK, 'fix'), wfRun(A, FORK, 'fix')), false)
})

test('matchesRun: same fork, different branch does not match', () => {
  assert.equal(matchesRun(pr(B, FORK, 'other'), wfRun(A, FORK, 'fix')), false)
})

test('matchesRun: run without head_repository falls back to the sha only', () => {
  assert.equal(matchesRun(pr(B, FORK, 'fix'), wfRun(A, null, 'fix')), false)
  assert.equal(matchesRun(pr(A, FORK, 'fix'), wfRun(A, null, 'fix')), true)
})

test('matchesRun: PR whose head repo is gone (deleted fork) falls back to the sha only', () => {
  assert.equal(matchesRun(pr(B, null, 'fix'), wfRun(A, FORK, 'fix')), false)
  assert.equal(matchesRun(pr(A, null, 'fix'), wfRun(A, FORK, 'fix')), true)
})

test('matchesRun: malformed input never matches', () => {
  assert.equal(matchesRun(null, wfRun(A, FORK, 'fix')), false)
  assert.equal(matchesRun(pr(A, FORK, 'fix'), null), false)
  assert.equal(matchesRun({}, wfRun(A, FORK, 'fix')), false)
})
