// unit/artifactConsole.test.js -- The Fleet Console's keystone is highlight-to-
// route: a text selection in a sandboxed artifact iframe must reach the parent
// (cross-iframe selections can't bubble, so the relayed postMessage is the only
// channel), and the resolved owner must be shown before send. These pure
// helpers encode that protocol; if they regress, routing silently breaks.

import { describe, it, expect } from 'vitest'

const mod = '../../../internal/web/static/app/panes/artifactConsole.js'

describe('parseSelectionMessage', () => {
  it('accepts a relay message for the open artifact and returns trimmed text', async () => {
    const { parseSelectionMessage, SELECTION_MESSAGE_TYPE } = await import(mod)
    const event = {
      data: {
        type: SELECTION_MESSAGE_TYPE,
        path: 'agent-deck/perf.html',
        text: '  ~14,000 queries for a 1,000-row batch  ',
        rect: { top: 10, left: 20 },
      },
    }
    const got = parseSelectionMessage(event, 'agent-deck/perf.html')
    expect(got).not.toBeNull()
    expect(got.text).toBe('~14,000 queries for a 1,000-row batch')
    expect(got.rect).toEqual({ top: 10, left: 20 })
  })

  it('ignores messages of the wrong type', async () => {
    const { parseSelectionMessage } = await import(mod)
    expect(parseSelectionMessage({ data: { type: 'other', text: 'x' } }, 'a')).toBeNull()
    expect(parseSelectionMessage({}, 'a')).toBeNull()
    expect(parseSelectionMessage(null, 'a')).toBeNull()
  })

  it('ignores a selection from a different artifact than the one in view', async () => {
    const { parseSelectionMessage, SELECTION_MESSAGE_TYPE } = await import(mod)
    const event = { data: { type: SELECTION_MESSAGE_TYPE, path: 'other/doc.html', text: 'hi' } }
    expect(parseSelectionMessage(event, 'agent-deck/perf.html')).toBeNull()
  })

  it('ignores an empty/whitespace selection', async () => {
    const { parseSelectionMessage, SELECTION_MESSAGE_TYPE } = await import(mod)
    const event = { data: { type: SELECTION_MESSAGE_TYPE, path: 'a', text: '   ' } }
    expect(parseSelectionMessage(event, 'a')).toBeNull()
  })
})

describe('ownerLabel (resolved target shown before send)', () => {
  it('prefers the sidecar session', async () => {
    const { ownerLabel } = await import(mod)
    expect(ownerLabel({ sessionId: 'sess-123', conductor: 'agent-deck' })).toBe('sess-123')
  })

  it('falls back to the owning conductor when there is no sidecar session', async () => {
    const { ownerLabel } = await import(mod)
    expect(ownerLabel({ conductor: 'innotrade' })).toBe('conductor-innotrade')
  })

  it('never leaves a dead end — prompts to choose when nothing is known', async () => {
    const { ownerLabel } = await import(mod)
    expect(ownerLabel(null)).toBe('choose a session')
    expect(ownerLabel({})).toBe('choose a session')
  })
})

describe('commentBody', () => {
  it('carries the artifact path + excerpt + comment for the server to resolve', async () => {
    const { commentBody } = await import(mod)
    const body = commentBody({ path: 'agent-deck/perf.html' }, 'the excerpt', 'the comment')
    expect(body).toEqual({ path: 'agent-deck/perf.html', excerpt: 'the excerpt', comment: 'the comment' })
  })
})

describe('serveUrl', () => {
  it('builds a confined serve url and appends a token when present', async () => {
    const { serveUrl } = await import(mod)
    expect(serveUrl({ path: 'agent-deck/perf.html' }, '')).toBe(
      '/api/artifacts/serve?path=agent-deck%2Fperf.html',
    )
    expect(serveUrl({ path: 'agent-deck/perf.html' }, 'tok')).toBe(
      '/api/artifacts/serve?path=agent-deck%2Fperf.html&token=tok',
    )
    expect(serveUrl(null, '')).toBe('')
  })
})
