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
import { useEffect, useState, useCallback } from 'preact/hooks'
import { menuModelSignal } from '../dataModel.js'
import { selectedIdSignal, mutationsEnabledSignal } from '../state.js'
import { addToast } from '../Toast.js'

const SCOPES = ['local', 'global', 'user']

async function jsonFetch(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  })
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (_) { /* ignore */ }
    throw new Error(msg)
  }
  if (res.status === 204) return null
  return res.json()
}

export function McpPane() {
  const { sessions } = menuModelSignal.value
  const selectedId = selectedIdSignal.value
  const mutationsEnabled = mutationsEnabledSignal.value
  const session = sessions.find(s => s.id === selectedId)

  const [catalog, setCatalog] = useState([])
  const [attached, setAttached] = useState({ local: [], global: [], user: [] })
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    if (!session) return
    setLoading(true)
    setError('')
    try {
      const [catalogResp, attachedResp] = await Promise.all([
        jsonFetch('/api/mcps'),
        jsonFetch(`/api/sessions/${encodeURIComponent(session.id)}/mcps`),
      ])
      setCatalog(catalogResp.mcps || [])
      setAttached({
        local: attachedResp.local || [],
        global: attachedResp.global || [],
        user: attachedResp.user || [],
      })
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [session && session.id])

  useEffect(() => { refresh() }, [refresh])

  const findScope = (name) => {
    for (const s of SCOPES) {
      if (attached[s].includes(name)) return s
    }
    return null
  }

  const attach = async (name, scope) => {
    if (!session) return
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
    if (!session) return
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
    if (!session) return
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

  return html`
    <div class="costs" data-testid="mcp-pane">
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
            mutationsEnabled=${mutationsEnabled}
            onDetach=${detach}
            onMove=${moveScope}/>

          <${CatalogSection}
            catalog=${catalog}
            attached=${attached}
            mutationsEnabled=${mutationsEnabled}
            onAttach=${attach}
            loading=${loading}/>
        </div>
      </div>
    </div>
  `
}

function AttachedSection({ attached, mutationsEnabled, onDetach, onMove }) {
  const allAttached = SCOPES.flatMap(scope =>
    attached[scope].map(name => ({ name, scope }))
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
              ${SCOPES.map(s => html`<option value=${s} key=${s}>${s}</option>`)}
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

function CatalogSection({ catalog, attached, mutationsEnabled, onAttach, loading }) {
  const isAttachedAnywhere = (name) => SCOPES.some(s => attached[s].includes(name))

  return html`
    <div data-testid="mcp-catalog">
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); letter-spacing: 0.08em; margin-bottom: 8px;">
        CATALOG (${catalog.length})
      </div>
      ${loading && html`<div style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); padding: 8px;">Loading…</div>`}
      ${!loading && catalog.length === 0 && html`
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding: 12px;">
          No MCPs in the catalog. Add some to <code>~/.agent-deck/config.toml</code>.
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
            <button disabled=${!mutationsEnabled || attachedHere}
                    data-testid=${`mcp-attach-${entry.name}`}
                    onClick=${() => onAttach(entry.name, 'local')}
                    style="font-family: var(--mono); font-size: 11px; background: ${attachedHere ? 'transparent' : 'var(--accent)'}; color: ${attachedHere ? 'var(--muted)' : 'var(--bg)'}; border: 1px solid ${attachedHere ? 'var(--border)' : 'var(--accent)'}; padding: 4px 12px; border-radius: 3px; cursor: ${attachedHere ? 'default' : 'pointer'};">
              ${attachedHere ? 'Attached' : 'Attach'}
            </button>
          </div>
        `
      })}
    </div>
  `
}
