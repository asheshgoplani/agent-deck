// Switching sessions while an MCP refresh is in flight must not let the old
// session's response land on the new one.
//
// The refresh is two async GETs. Before this guard, a slow response for session
// A could resolve after the user had already selected session B and overwrite
// B's catalog, attachments and — the damaging part — B's SCOPE LIST. The next
// attach then used A's default scope, which B's tool may not even have: a
// Claude session leaves "local" behind, and a Codex session (global-only) would
// send "local" and be refused.
//
// Asserts mcpStateForResponse directly rather than rendering McpPane: rendering
// preact components under this vitest alias map throws from @preact/signals (see
// unit/archivedPane.test.js for the same constraint). This function is the whole
// of the stale-response decision.
import { describe, expect, it } from 'vitest'

const mcpPaneModulePath = '../../../internal/web/static/app/panes/McpPane.js'

const claudeResponse = {
  sessionId: 'sess-claude',
  local: ['exa'],
  project: [],
  global: [],
  user: [],
  scopes: ['local', 'project', 'global', 'user'],
}

const codexResponse = {
  sessionId: 'sess-codex',
  local: [],
  project: [],
  global: ['youtube'],
  user: [],
  scopes: ['global'],
}

const catalog = { mcps: [{ name: 'exa' }, { name: 'youtube' }] }

describe('mcpStateForResponse — session re-keying', () => {
  it('discards a response whose session is no longer selected', async () => {
    const { mcpStateForResponse } = await import(mcpPaneModulePath)

    // The refresh was started for the Claude session; by the time it resolved
    // the user had switched to the Codex one.
    const stale = mcpStateForResponse({
      forSessionId: 'sess-claude',
      activeSessionId: 'sess-codex',
      catalogResp: catalog,
      attachedResp: claudeResponse,
    })

    expect(stale).toBeNull()
  })

  it('applies a response for the session that is still selected', async () => {
    const { mcpStateForResponse } = await import(mcpPaneModulePath)

    const fresh = mcpStateForResponse({
      forSessionId: 'sess-codex',
      activeSessionId: 'sess-codex',
      catalogResp: catalog,
      attachedResp: codexResponse,
    })

    expect(fresh).not.toBeNull()
    expect(fresh.sessionId).toBe('sess-codex')
    expect(fresh.scopes).toEqual(['global'])
    expect(fresh.attached.global).toEqual(['youtube'])
  })

  it('an attach after a mid-flight switch uses the NEW session’s scope', async () => {
    const { mcpStateForResponse } = await import(mcpPaneModulePath)

    // Order of arrival: the user switches Claude -> Codex, the Codex response
    // lands first, then the stale Claude response arrives late.
    let state = mcpStateForResponse({
      forSessionId: 'sess-codex',
      activeSessionId: 'sess-codex',
      catalogResp: catalog,
      attachedResp: codexResponse,
    })
    expect(state.defaultScope).toBe('global')

    const late = mcpStateForResponse({
      forSessionId: 'sess-claude',
      activeSessionId: 'sess-codex',
      catalogResp: catalog,
      attachedResp: claudeResponse,
    })
    // Null means the component keeps the state it already has.
    expect(late).toBeNull()
    if (late) state = late

    // The scope an attach would now send. "local" here would be refused by the
    // server, because Codex has no local store.
    expect(state.defaultScope).toBe('global')
    expect(state.scopes).not.toContain('local')
  })

  it('a session with no id never applies a response', async () => {
    const { mcpStateForResponse } = await import(mcpPaneModulePath)

    expect(mcpStateForResponse({
      forSessionId: null,
      activeSessionId: null,
      catalogResp: catalog,
      attachedResp: claudeResponse,
    })).toBeNull()
  })

  it('falls back to every scope when an older server omits the field', async () => {
    const { mcpStateForResponse } = await import(mcpPaneModulePath)

    const state = mcpStateForResponse({
      forSessionId: 'sess-claude',
      activeSessionId: 'sess-claude',
      catalogResp: catalog,
      attachedResp: { local: [], project: [], global: [], user: [] },
    })

    expect(state.scopes).toEqual(['local', 'project', 'global', 'user'])
    expect(state.defaultScope).toBe('local')
  })
})
