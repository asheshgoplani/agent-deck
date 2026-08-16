// panes/McpPane.js -- Web UI for MCP management.
//
// Mirrors the TUI `m` key dialog (internal/ui/mcp_dialog.go). Closes the
// four MISSING rows under "MCP MANAGEMENT" in tests/web/PARITY_MATRIX.md.
//
// Endpoints used:
//   GET    /api/mcps                              -> catalog
//   GET    /api/sessions/{id}/mcps                -> attached
//   POST   /api/sessions/{id}/mcps/{name}         -> attach (scope in body)
//   DELETE /api/sessions/{id}/mcps/{name}         -> detach (scope in body)
//   PATCH  /api/sessions/{id}/mcps/{name}         -> move scope (toggle pooled ↔ local)
import { html } from 'htm/preact'
import { useEffect, useState, useCallback, useRef } from 'preact/hooks'
import { menuModelSignal } from '../dataModel.js'
import { selectedIdSignal, mutationsEnabledSignal } from '../state.js'
import { addToast } from '../Toast.js'
import { authHeaders } from '../api.js'

// Every scope the API can report, most specific first. Which of these a given
// session actually has is decided server-side and returned as `scopes` on
// GET /api/sessions/{id}/mcps — Codex and Gemini are global-only, Cursor and
// OpenCode have no user scope, and `project` (Claude's
// projects[path].mcpServers) is distinct from `local` (<project>/.mcp.json).
// Never hardcode a scope: doing so made every Codex/Gemini attach fail.
const ALL_SCOPES = ['local', 'project', 'global', 'user']
const EMPTY_ATTACHED = { local: [], project: [], global: [], user: [] }

// Whether a session's tool has any MCP store is decided server-side and
// delivered as `mcpSupported` on the menu session (see MenuSession in
// internal/web/session_data_service.go). Do NOT reimplement the predicate
// here: session.ToolSupportsMCPManager is config-driven, so a user tool
// declaring compatible_with = "claude" is supported even though its name is
// not in any hardcoded list.
function toolSupportsMCP(session) {
  return session.mcpSupported !== false
}


// mcpStateForResponse reduces a completed refresh into pane state, or returns
// null when the response belongs to a session the user has already navigated
// away from.
//
// Refreshes are async, so switching sessions mid-flight used to let the old
// session's catalog, attachments and — worst — its SCOPE LIST land on the new
// session. The next attach then used a scope the new session's tool may not
// have. Responses are keyed by session id and mismatches are discarded.
//
// Exported for tests: rendering preact components under vitest is broken in
// this repo (see unit/archivedPane.test.js), so the logic that matters is
// asserted directly.
export function mcpStateForResponse({ forSessionId, activeSessionId, catalogResp, attachedResp }) {
  if (!forSessionId || forSessionId !== activeSessionId) return null
  const scopes = attachedResp && attachedResp.scopes && attachedResp.scopes.length
    ? attachedResp.scopes
    : ALL_SCOPES
  return {
    sessionId: forSessionId,
    catalog: (catalogResp && catalogResp.mcps) || [],
    attached: {
      local: (attachedResp && attachedResp.local) || [],
      project: (attachedResp && attachedResp.project) || [],
      global: (attachedResp && attachedResp.global) || [],
      user: (attachedResp && attachedResp.user) || [],
    },
    scopes,
    defaultScope: scopes[0] || '',
  }
}

// createRefreshGate decides, for every async refresh, two things: may this
// refresh start, and may its result still be applied?
//
// mcpStateForResponse above rejects a response belonging to a session the user
// has left. It cannot reject a STALE response for the session the user is still
// on: two refreshes for the same session (a switch and an attach, say) both pass
// the session-id check, so whichever resolves last wins even when it is the
// older request, reinstating a pre-mutation scope list.
//
// Tokens are keyed BY SESSION, and begin() refuses — without touching any token
// — when the refresh belongs to a session that is no longer active. A single
// shared counter would be worse than none: an attach on session A that resolves
// after the user switches to B calls refresh() for A, and a shared counter would
// advance past B's in-flight token, discarding B's legitimate response and
// leaving B with empty scopes and the spinner stuck on. That is a stale refresh
// POISONING a newer session's, a different failure from a stale response merely
// winning, and not fixed by the same check. Checking before advancing, and
// keying per session, prevents both.
//
// Exported for tests, like mcpStateForResponse: rendering preact components
// under vitest is broken here (see unit/archivedPane.test.js).
export function createRefreshGate() {
  const tokens = new Map()
  return {
    // begin returns a token, or null when this refresh must not run at all.
    // Callers that get null must return immediately: no fetch, no loading
    // state, no token consumed.
    begin(forSessionId, activeSessionId) {
      if (!forSessionId || forSessionId !== activeSessionId) return null
      const token = (tokens.get(forSessionId) || 0) + 1
      tokens.set(forSessionId, token)
      return token
    },
    // mayApply is true only for the newest refresh of the still-active session.
    mayApply(forSessionId, activeSessionId, token) {
      if (token == null || forSessionId !== activeSessionId) return false
      return tokens.get(forSessionId) === token
    },
    // forget drops a session's token so the map cannot grow for the lifetime of
    // the tab. A refresh still in flight for that session then fails mayApply,
    // which is correct: the user is no longer looking at it.
    forget(sessionId) {
      tokens.delete(sessionId)
    },
  }
}

// jsonFetch keeps this pane's 204 handling and error shaping, but the headers
// (and therefore the bearer token) come from the shared helper. Building them
// locally is what made every MCP call 401 against an authenticated server.
async function jsonFetch(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: authHeaders(opts.headers),
  })
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const body = await res.json()
      // The API error envelope is {error: {code, message}}; reading
      // body.error directly rendered "[object Object]" and hid the reason.
      const apiMsg = body && body.error && body.error.message
      if (apiMsg) msg = apiMsg
    } catch (_) { /* ignore */ }
    throw new Error(msg)
  }
  if (res.status === 204) return null
  return res.json()
}

// McpPane resolves the selected session and hands off to a child keyed by
// session id.
//
// The key is the fix for the stale-frame window: resetting per-session state in
// a useEffect runs AFTER the render commits, so the first frame of a new
// session painted the previous session's catalog, attachments and scopes while
// the mutation callbacks were already bound to the new session. A click landing
// in that frame mutated the new session using the old session's data. Keying
// the child means switching sessions unmounts it and mounts a fresh one, so the
// old state cannot be painted at all — the reset is structural, not deferred.
export function McpPane() {
  const { sessions } = menuModelSignal.value
  const selectedId = selectedIdSignal.value
  const session = sessions.find(s => s.id === selectedId)

  if (!session) {
    return html`
      <div class="costs">
        <div class="chart-card" style="text-align: center; padding: 48px 24px;">
          <div class="title" style="font-size: 16px;">MCP Manager</div>
          <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding-top: 8px;">
            Select a session in the sidebar to manage MCPs.
          </div>
        </div>
      </div>
    `
  }

  // Say so rather than rendering a catalog whose buttons the server will
  // refuse. The tool decides which MCP store exists; a shell session has none.
  if (!toolSupportsMCP(session)) {
    return html`
      <div class="costs" data-testid="mcp-pane" data-session-id=${session.id}>
        <div class="chart-card" style="text-align: center; padding: 48px 24px;">
          <div class="title" style="font-size: 16px;">MCP Manager</div>
          <div data-testid="mcp-unsupported-tool"
               style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding-top: 8px;">
            MCP management is not available for ${session.tool || 'this'} sessions.
            Supported tools: Claude, Codex, Gemini, Cursor, OpenCode.
          </div>
        </div>
      </div>
    `
  }

  return html`<${McpPaneForSession} key=${session.id} session=${session}/>`
}

// McpPaneForSession owns all per-session state. It is mounted fresh for each
// session (see the key above), so every useState below starts empty and no
// value can leak across a switch.
function McpPaneForSession({ session }) {
  const mutationsEnabled = mutationsEnabledSignal.value
  const sessionId = session.id
  // Still keyed defensively: a refresh in flight when this instance unmounts
  // must not apply, and mcpStateForResponse is the tested seam for that.
  const activeSessionRef = useRef(sessionId)
  // One gate per mounted pane; see createRefreshGate.
  const gateRef = useRef(null)
  if (gateRef.current === null) gateRef.current = createRefreshGate()

  const [catalog, setCatalog] = useState([])
  const [attached, setAttached] = useState(EMPTY_ATTACHED)
  // Scopes this session's tool actually has, as reported by the server.
  const [scopes, setScopes] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    const forSession = sessionId
    // Asked BEFORE any shared state is touched. A refresh for a session the
    // user has already left must not consume a token, set loading, or fetch.
    const token = gateRef.current.begin(forSession, activeSessionRef.current)
    if (token === null) return
    setLoading(true)
    setError('')
    try {
      const [catalogResp, attachedResp] = await Promise.all([
        jsonFetch('/api/mcps'),
        jsonFetch(`/api/sessions/${encodeURIComponent(session.id)}/mcps`),
      ])
      const next = mcpStateForResponse({
        forSessionId: forSession,
        activeSessionId: activeSessionRef.current,
        catalogResp,
        attachedResp,
      })
      // Stale: the user switched sessions while this was in flight, or a newer
      // refresh for this same session has already been issued.
      if (!next || !gateRef.current.mayApply(forSession, activeSessionRef.current, token)) return
      setCatalog(next.catalog)
      setAttached(next.attached)
      setScopes(next.scopes)
    } catch (err) {
      if (gateRef.current.mayApply(forSession, activeSessionRef.current, token)) setError(err.message)
    } finally {
      // Only the newest refresh owns the spinner; an older one clearing it
      // would show "done" while the current request is still in flight.
      if (gateRef.current.mayApply(forSession, activeSessionRef.current, token)) setLoading(false)
    }
  }, [sessionId])

  useEffect(() => {
    const previous = activeSessionRef.current
    // Release the token of the session being left, so the map cannot grow for
    // the lifetime of the tab. Only the departing session is forgotten.
    if (previous && previous !== sessionId) gateRef.current.forget(previous)
    activeSessionRef.current = sessionId
    refresh()
    return () => { activeSessionRef.current = null }
  }, [sessionId, refresh])

  const findScope = (name) => {
    for (const s of ALL_SCOPES) {
      if ((attached[s] || []).includes(name)) return s
    }
    return null
  }

  // The scope a fresh attach lands in: the tool's most specific store.
  const defaultScope = scopes[0] || ''

  const attach = async (name, scope) => {
    try {
      await jsonFetch(`/api/sessions/${encodeURIComponent(session.id)}/mcps/${encodeURIComponent(name)}`, {
        method: 'POST',
        body: JSON.stringify({ scope }),
      })
      addToast(`Attached ${name} (${scope})`, 'success')
      await refresh()
    } catch (err) {
      addToast(`Attach failed: ${err.message}`, 'error')
    }
  }

  const detach = async (name) => {
    const scope = findScope(name)
    try {
      await jsonFetch(`/api/sessions/${encodeURIComponent(session.id)}/mcps/${encodeURIComponent(name)}`, {
        method: 'DELETE',
        body: scope ? JSON.stringify({ scope }) : '',
      })
      addToast(`Detached ${name}`, 'success')
      await refresh()
    } catch (err) {
      addToast(`Detach failed: ${err.message}`, 'error')
    }
  }

  const moveScope = async (name, toScope) => {
    try {
      await jsonFetch(`/api/sessions/${encodeURIComponent(session.id)}/mcps/${encodeURIComponent(name)}`, {
        method: 'PATCH',
        body: JSON.stringify({ scope: toScope }),
      })
      addToast(`Moved ${name} → ${toScope}`, 'success')
      await refresh()
    } catch (err) {
      addToast(`Move failed: ${err.message}`, 'error')
    }
  }

  return html`
    <div class="costs" data-testid="mcp-pane" data-session-id=${sessionId}>
      <div class="chart-card" style="padding: 24px;">
        <div class="title" style="font-size: 16px; margin-bottom: 4px;">MCP Manager</div>
        <div style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); margin-bottom: 16px;">
          ${session.title} · ${session.path || ''}
        </div>

        ${error && html`
          <div style="font-family: var(--mono); font-size: 11px; color: var(--err); background: var(--err-bg); padding: 8px 12px; border-radius: 4px; margin-bottom: 12px;" data-testid="mcp-error">
            ${error}
          </div>
        `}

        <div style="display: grid; grid-template-columns: 1fr; gap: 24px;">
          <${AttachedSection}
            attached=${attached}
            scopes=${scopes}
            mutationsEnabled=${mutationsEnabled}
            onDetach=${detach}
            onMove=${moveScope}/>

          <${CatalogSection}
            catalog=${catalog}
            attached=${attached}
            scopes=${scopes}
            defaultScope=${defaultScope}
            mutationsEnabled=${mutationsEnabled}
            onAttach=${attach}
            loading=${loading}/>
        </div>
      </div>
    </div>
  `
}

function AttachedSection({ attached, scopes, mutationsEnabled, onDetach, onMove }) {
  const allAttached = ALL_SCOPES.flatMap(scope =>
    (attached[scope] || []).map(name => ({ name, scope }))
  )

  return html`
    <div data-testid="mcp-attached">
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); letter-spacing: 0.08em; margin-bottom: 8px;">
        ATTACHED (${allAttached.length})
      </div>
      ${allAttached.length === 0 && html`
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding: 12px;">
          No MCPs attached. Use the catalog below to attach.
        </div>
      `}
      ${allAttached.map(({ name, scope }) => html`
        <div key=${`${scope}-${name}`} data-testid=${`mcp-attached-${name}`}
             style="display: flex; align-items: center; justify-content: space-between; padding: 8px 12px; border: 1px solid var(--border); border-radius: 4px; margin-bottom: 6px;">
          <div>
            <span style="font-family: var(--mono); font-size: 13px; color: var(--text);">${name}</span>
            <span style="font-family: var(--mono); font-size: 10px; color: var(--muted); margin-left: 8px; letter-spacing: 0.08em;">
              ${scope.toUpperCase()}
            </span>
          </div>
          <div style="display: flex; gap: 6px;">
            <select disabled=${!mutationsEnabled}
                    data-testid=${`mcp-scope-${name}`}
                    value=${scope}
                    onChange=${e => onMove(name, e.target.value)}
                    style="font-family: var(--mono); font-size: 11px; background: var(--bg); color: var(--text); border: 1px solid var(--border); padding: 2px 6px; border-radius: 3px;">
              ${(scopes.length ? scopes : ALL_SCOPES).map(s => html`<option value=${s} key=${s}>${s}</option>`)}
            </select>
            <button disabled=${!mutationsEnabled}
                    data-testid=${`mcp-detach-${name}`}
                    onClick=${() => onDetach(name)}
                    style="font-family: var(--mono); font-size: 11px; background: transparent; color: var(--err); border: 1px solid var(--err); padding: 2px 8px; border-radius: 3px; cursor: pointer;">
              Detach
            </button>
          </div>
        </div>
      `)}
    </div>
  `
}

function CatalogSection({ catalog, attached, scopes, defaultScope, mutationsEnabled, onAttach, loading }) {
  const isAttachedAnywhere = (name) => ALL_SCOPES.some(s => (attached[s] || []).includes(name))

  return html`
    <div data-testid="mcp-catalog">
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); letter-spacing: 0.08em; margin-bottom: 8px;">
        CATALOG (${catalog.length})
      </div>
      ${loading && html`<div style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); padding: 8px;">Loading…</div>`}
      ${!loading && catalog.length === 0 && html`
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding: 12px;">
          No MCPs in the catalog. Add some to <code>~/.config/agent-deck/config.toml</code>.
        </div>
      `}
      ${catalog.map(entry => {
        const attachedHere = isAttachedAnywhere(entry.name)
        return html`
          <div key=${entry.name} data-testid=${`mcp-catalog-${entry.name}`}
               style="display: flex; align-items: center; justify-content: space-between; padding: 8px 12px; border: 1px solid var(--border); border-radius: 4px; margin-bottom: 6px;">
            <div style="display: flex; flex-direction: column;">
              <span style="font-family: var(--mono); font-size: 13px; color: var(--text);">${entry.name}</span>
              ${entry.description && html`<span style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); margin-top: 2px;">${entry.description}</span>`}
              <span style="font-family: var(--mono); font-size: 10px; color: var(--muted); margin-top: 2px; letter-spacing: 0.06em;">
                ${(entry.transport || 'stdio').toUpperCase()}${entry.command ? ` · ${entry.command}` : ''}
              </span>
            </div>
            <button disabled=${!mutationsEnabled || attachedHere || !defaultScope}
                    data-testid=${`mcp-attach-${entry.name}`}
                    onClick=${() => onAttach(entry.name, defaultScope)}
                    style="font-family: var(--mono); font-size: 11px; background: ${attachedHere ? 'transparent' : 'var(--accent)'}; color: ${attachedHere ? 'var(--muted)' : 'var(--bg)'}; border: 1px solid ${attachedHere ? 'var(--border)' : 'var(--accent)'}; padding: 4px 12px; border-radius: 3px; cursor: ${attachedHere ? 'default' : 'pointer'};">
              ${attachedHere ? 'Attached' : 'Attach'}
            </button>
          </div>
        `
      })}
    </div>
  `
}
