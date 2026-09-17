// tests/ci/ci-stalled-notify.test.cjs -- unit tests for the stall-label
// reconciliation used by .github/workflows/ci-stalled-notify.yml.
// Run: node --test tests/ci/ci-stalled-notify.test.cjs

'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const { reconcile } = require('../../.github/scripts/ci-stalled-notify.cjs')

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

test('stalled run on an already labeled PR does nothing', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: A, labeled: true,
    currentHeadRuns: [], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /already labeled/)
})

test('delayed completion for an old head never clears a stall on the new head', () => {
  // Head A finished late; the PR has moved to head B, which is stalled.
  const r = reconcile({
    runHeadSha: A, runConclusion: 'success', prHeadSha: B, labeled: true,
    currentHeadRuns: [run('Go tests', B, 'action_required')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /stale event/)
})

test('a stale action_required event for an old head does not add the label either', () => {
  const r = reconcile({
    runHeadSha: A, runConclusion: 'action_required', prHeadSha: B, labeled: false,
    currentHeadRuns: [run('Go tests', B, 'success')], gatingWorkflows: GATES,
  })
  assert.equal(r.action, 'none')
  assert.match(r.reason, /stale event/)
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
  assert.match(r.reason, /Go tests/)
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
