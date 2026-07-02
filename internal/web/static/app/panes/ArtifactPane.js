// panes/ArtifactPane.js -- The Fleet Console "Artifacts" tab.
//
// This kills the two pains the console exists for:
//  1. TAB EXPLOSION: every conductor HTML renders as an INLINE CARD here (in a
//     sandboxed iframe), never a new browser tab.
//  2. MANUAL ROUTING: highlight a passage in a card and the annotation auto-
//     routes to the artifact's owning session — the resolved owner is shown
//     BEFORE you send, so you never hunt for the session.
//
// The artifact runs in an opaque-origin sandbox (allow-scripts, NO
// allow-same-origin): its scripts — including the server-injected selection
// relay — cannot touch this origin, the parent DOM, or any credential. The
// relay postMessages each selection out (cross-iframe selections can't bubble).
import { html } from 'htm/preact'
import { useEffect, useState, useCallback } from 'preact/hooks'
import { authTokenSignal, mutationsEnabledSignal } from '../state.js'
import { apiFetch } from '../api.js'
import { addToast } from '../Toast.js'
import { parseSelectionMessage, ownerLabel, commentBody, serveUrl } from './artifactConsole.js'

const C = {
  bg: '#0d1117', elev: '#161b22', elev2: '#1c2128', border: '#30363d',
  text: '#e6edf3', dim: '#8b949e', faint: '#6e7681', accent: '#2f81f7',
  routed: '#388bfd1a', routedBorder: '#2f81f7', good: '#79c0ff',
}

function ArtifactCard({ entry, token, onExpand, expanded }) {
  const owner = ownerLabel(entry)
  const prov = entry.hasSidecar ? '.meta.json ✓ stamped' : 'conductor-level (no sidecar)'
  return html`
    <div class="art-card" data-testid="artifact-card" data-path=${entry.path}
      style=${{ border: `1px solid ${C.border}`, borderRadius: '10px', overflow: 'hidden',
        background: C.elev, margin: '0 0 14px', maxWidth: '900px' }}>
      <div style=${{ display: 'flex', alignItems: 'center', gap: '10px', padding: '9px 13px',
        background: C.elev2, borderBottom: `1px solid ${C.border}` }}>
        <span>📄</span>
        <span style=${{ fontWeight: 600, fontSize: '12.5px', color: C.text }}>${entry.title}</span>
        <span data-testid="artifact-owner-chip" style=${{ display: 'flex', alignItems: 'center', gap: '6px',
          fontSize: '11px', color: C.dim, background: C.bg, border: `1px solid ${C.border}`,
          borderRadius: '999px', padding: '2px 9px' }}>↳ ${owner}</span>
        <span style=${{ flex: 1 }}></span>
        <span style=${{ fontSize: '10px', color: C.faint, fontFamily: 'monospace' }}>${prov}</span>
        <button class="art-expand" onClick=${() => onExpand(entry.path)}
          style=${{ fontSize: '11px', color: C.dim, border: `1px solid ${C.border}`, background: C.bg,
            borderRadius: '5px', padding: '2px 8px', cursor: 'pointer' }}>
          ${expanded ? '▾ Collapse' : '▸ Render'}
        </button>
      </div>
      ${expanded && html`
        <iframe data-testid="artifact-frame" title=${entry.title}
          sandbox="allow-scripts"
          src=${serveUrl(entry, token)}
          style=${{ width: '100%', height: '420px', border: 'none', background: '#0a0c10' }}></iframe>
      `}
    </div>
  `
}

export function ArtifactPane() {
  const token = authTokenSignal.value
  const canMutate = mutationsEnabledSignal.value
  const [entries, setEntries] = useState([])
  const [loaded, setLoaded] = useState(false)
  const [expanded, setExpanded] = useState({})
  const [sel, setSel] = useState(null)       // { entry, text } pending annotation
  const [comment, setComment] = useState('')
  const [routed, setRouted] = useState([])   // resolved annotations (audit trail)
  const [sending, setSending] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const resp = await apiFetch('GET', '/api/artifacts')
      setEntries(Array.isArray(resp.artifacts) ? resp.artifacts : [])
    } catch (_) {
      setEntries([])
    } finally {
      setLoaded(true)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  // Selection relay: the only channel for a cross-iframe highlight to reach us.
  useEffect(() => {
    const onMessage = (event) => {
      const parsed = parseSelectionMessage(event, null)
      if (!parsed) return
      const entry = entries.find(e => e.path === parsed.path)
      if (!entry) return
      setSel({ entry, text: parsed.text })
      setComment('')
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [entries])

  const onExpand = (path) => setExpanded(m => ({ ...m, [path]: !m[path] }))

  const send = async () => {
    if (!sel || sending) return
    const note = comment.trim()
    if (!note) return
    if (!canMutate) {
      addToast('Commenting is disabled (web mutations off)', 'info')
      return
    }
    setSending(true)
    try {
      const resp = await apiFetch('POST', '/api/artifacts/comment', commentBody(sel.entry, sel.text, note))
      if (resp.needsPicker) {
        addToast('Could not auto-resolve the owner — pick a session', 'info')
        setRouted(r => [{ excerpt: sel.text, note, label: '(needs a session choice)', kind: 'picker',
          candidates: resp.candidates || [] }, ...r])
      } else {
        addToast(`Routed to ${resp.label || resp.routedTo}${resp.busy ? ' (durable inbox)' : ''}`, 'success')
        setRouted(r => [{ excerpt: sel.text, note, label: resp.label || resp.routedTo,
          kind: resp.kind, busy: resp.busy }, ...r])
      }
      setSel(null)
      setComment('')
    } catch (e) {
      addToast(e.message || 'comment failed', 'error')
    } finally {
      setSending(false)
    }
  }

  return html`
    <div class="artifacts" data-testid="artifact-pane"
      style=${{ display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1, color: C.text }}>
      <div style=${{ display: 'flex', alignItems: 'center', gap: '12px', padding: '12px 18px',
        borderBottom: `1px solid ${C.border}`, background: C.elev }}>
        <h1 style=${{ fontSize: '15px', fontWeight: 600 }}>Artifacts</h1>
        <span style=${{ fontSize: '11.5px', color: C.dim }} data-testid="artifact-count">
          ${entries.length} inline · no browser tabs
        </span>
        <span style=${{ flex: 1 }}></span>
        <button onClick=${refresh} data-testid="artifact-refresh"
          style=${{ fontSize: '11.5px', color: C.dim, border: `1px solid ${C.border}`,
            background: C.elev2, borderRadius: '6px', padding: '4px 10px', cursor: 'pointer' }}>↻ Refresh</button>
      </div>

      <div style=${{ overflowY: 'auto', flex: 1, padding: '18px 22px' }}>
        ${!loaded && html`<div data-testid="artifact-loading" style=${{ color: C.faint }}>Loading artifacts…</div>`}
        ${loaded && entries.length === 0 && html`
          <div data-testid="artifact-empty" style=${{ color: C.faint }}>
            No artifacts yet. A session that writes an HTML report and runs
            <code style=${{ fontFamily: 'monospace', color: C.dim }}>agent-deck artifact stamp</code>
            will appear here as an inline card.
          </div>`}

        ${routed.length > 0 && html`
          <div data-testid="artifact-routed-list" style=${{ marginBottom: '14px' }}>
            ${routed.map((r, i) => html`
              <div key=${i} data-testid="artifact-routed-note"
                style=${{ maxWidth: '900px', margin: '0 0 8px', display: 'flex', gap: '9px',
                  alignItems: 'flex-start', background: C.routed, border: `1px solid ${C.routedBorder}4d`,
                  borderRadius: '8px', padding: '9px 12px' }}>
                <span style=${{ color: C.accent }}>↳</span>
                <div style=${{ fontSize: '12px', color: C.dim }}>
                  Highlight “<span style=${{ color: C.good }}>${r.excerpt}</span>” routed to
                  <b style=${{ color: C.good }}> ${r.label}</b>
                  ${r.busy ? ' · via durable inbox' : ''}: “${r.note}”
                </div>
              </div>
            `)}
          </div>`}

        ${entries.map(e => html`
          <${ArtifactCard} key=${e.path} entry=${e} token=${token}
            expanded=${!!expanded[e.path]} onExpand=${onExpand}/>
        `)}
      </div>

      ${sel && html`
        <div data-testid="artifact-annotation-bubble"
          style=${{ borderTop: `1px solid ${C.routedBorder}`, background: C.elev2, padding: '12px 18px' }}>
          <div style=${{ fontSize: '11.5px', color: C.dim, borderLeft: `2px solid ${C.routedBorder}`,
            paddingLeft: '9px', marginBottom: '8px', fontStyle: 'italic' }}>“${sel.text}”</div>
          <div data-testid="artifact-route-target"
            style=${{ display: 'flex', alignItems: 'center', gap: '7px', fontSize: '11.5px',
              background: C.routed, border: `1px solid ${C.routedBorder}`, borderRadius: '6px',
              padding: '6px 9px', marginBottom: '9px' }}>
            <span style=${{ color: C.accent, fontWeight: 700 }}>↳ routes to</span>
            <b style=${{ color: C.good }}>${ownerLabel(sel.entry)}</b>
          </div>
          <div style=${{ display: 'flex', gap: '9px', alignItems: 'flex-end' }}>
            <textarea data-testid="artifact-comment-input"
              placeholder="Comment — auto-routes to the owning session via the durable inbox…"
              value=${comment} onInput=${e => setComment(e.target.value)}
              style=${{ flex: 1, background: C.bg, border: `1px solid ${C.border}`, borderRadius: '8px',
                color: C.text, fontSize: '13px', padding: '9px 12px', resize: 'none', height: '46px' }}></textarea>
            <button data-testid="artifact-comment-send" disabled=${!comment.trim() || sending} onClick=${send}
              style=${{ background: C.accent, color: '#fff', border: 'none', borderRadius: '8px',
                padding: '9px 18px', fontSize: '13px', fontWeight: 600, cursor: 'pointer', height: '46px' }}>
              ${sending ? 'Sending…' : 'Send ▸'}</button>
            <button data-testid="artifact-comment-cancel" onClick=${() => setSel(null)}
              style=${{ background: C.elev, color: C.dim, border: `1px solid ${C.border}`, borderRadius: '8px',
                padding: '9px 12px', fontSize: '13px', cursor: 'pointer', height: '46px' }}>✕</button>
          </div>
        </div>`}
    </div>
  `
}
