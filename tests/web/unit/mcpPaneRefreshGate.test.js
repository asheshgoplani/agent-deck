// Regression tests for the refresh gate in McpPane (issue #1956 follow-up).
//
// mcpStateForResponse (covered by mcpPaneSessionSwitch.test.js) answers "does
// this response belong to the session on screen?". The gate answers the two
// questions it cannot: "may this refresh start at all?" and "is this still the
// newest refresh for that session?".
//
// The bug that motivates the second question is subtle and is NOT fixed by the
// session-id check alone: an attach/detach/move started on session A finishes
// after the user has switched to session B, and calls refresh() for A. With a
// single shared counter, that late refresh advances the counter past B's
// in-flight token, so B's own legitimate response is then discarded — leaving B
// with empty scopes and the spinner stuck on. A stale response POISONING a
// newer session's refresh, rather than merely winning over it.
import { describe, expect, it } from 'vitest'

const mcpPaneModulePath = '../../../internal/web/static/app/panes/McpPane.js'

describe('createRefreshGate', () => {
  it('refuses a refresh for a session that is no longer active', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    expect(gate.begin('A', 'B')).toBe(null)
    expect(gate.begin(null, null)).toBe(null)
  })

  it('does not consume a token when it refuses', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    // B's legitimate refresh is in flight.
    const bToken = gate.begin('B', 'B')
    expect(bToken).not.toBe(null)

    // A late attach on the session the user LEFT calls refresh() for A.
    // Checking before advancing is the whole point: this must not disturb B.
    expect(gate.begin('A', 'B')).toBe(null)

    // B's response is still the one to apply. This is the regression: with a
    // single shared counter, B's token would already be stale here and B would
    // be left with empty scopes and loading stuck on.
    expect(gate.mayApply('B', 'B', bToken)).toBe(true)
  })

  it('keeps tokens per session, so one session cannot stale another', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    const a1 = gate.begin('A', 'A')
    const b1 = gate.begin('B', 'B')

    // Each is the newest for its own session; neither advanced the other.
    expect(gate.mayApply('A', 'A', a1)).toBe(true)
    expect(gate.mayApply('B', 'B', b1)).toBe(true)
  })

  it('lets only the newest refresh of a session apply', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    // Two refreshes for the SAME session — a switch and an attach, say. Both
    // pass the session-id check, so without a token the older response would
    // win when it resolves last and reinstate a pre-mutation scope list.
    const first = gate.begin('A', 'A')
    const second = gate.begin('A', 'A')

    expect(gate.mayApply('A', 'A', first)).toBe(false)
    expect(gate.mayApply('A', 'A', second)).toBe(true)
  })

  it('rejects a response whose session is no longer active', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    const token = gate.begin('A', 'A')
    // User switched to B while A's refresh was in flight.
    expect(gate.mayApply('A', 'B', token)).toBe(false)
  })

  it('treats a null token as never applicable', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    // begin() returned null and the caller ignored it: still must not apply.
    expect(gate.mayApply('A', 'A', null)).toBe(false)
    expect(gate.mayApply('A', 'A', undefined)).toBe(false)
  })

  it('forgets a session without stranding the active one', async () => {
    const { createRefreshGate } = await import(mcpPaneModulePath)
    const gate = createRefreshGate()

    const a1 = gate.begin('A', 'A')
    const b1 = gate.begin('B', 'B')

    gate.forget('A')

    // A's in-flight refresh can no longer apply, which is correct: the user is
    // not looking at A. B is untouched.
    expect(gate.mayApply('A', 'A', a1)).toBe(false)
    expect(gate.mayApply('B', 'B', b1)).toBe(true)
  })
})
