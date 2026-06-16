// panes/GenuiPane.js -- The generative command center (v-genui-0 + v-genui-1).
//
// genui-0 MAGIC: the SAME live fleet data, rendered by 3 hand-authored whole-UI
// SPECS. Switching specs reshapes the ENTIRE view live — no rebuild, no new
// code. The fixed engine (GenuiRenderer + the Go validator) makes that safe:
// specs are inert DATA the renderer interprets.
//
// genui-1 ADDS: type an INTENT ("show me what's blocked") and a pluggable
// server-side composer EMITS a spec from it. That generated spec is just
// another spec — it passes the SAME Go validator (with a bounded repair loop)
// before it is returned, so the renderer only ever draws vetted output. A
// rejected/un-repairable compose surfaces a clean error; it is never rendered.
//
// Data flows in by REFERENCE: this pane projects the live commandCenterSignal
// snapshot (delivered over the existing /events/command-center SSE) into the
// secret-free binding object the spec refs resolve against.
import { html } from 'htm/preact'
import { useState, useEffect } from 'preact/hooks'
import { commandCenterSignal, connectionSignal, authTokenSignal } from '../state.js'
import { renderSpec } from '../genui/GenuiRenderer.js'

// fetchJSON loads a server-validated spec (or the view list). The token goes in
// the Authorization header so it never leaks to logs/Referer.
async function fetchJSON(path) {
  const headers = { Accept: 'application/json' }
  const tok = authTokenSignal.value
  if (tok) headers['Authorization'] = 'Bearer ' + tok
  const res = await fetch(path, { headers })
  if (!res.ok) throw new Error('HTTP ' + res.status)
  return res.json()
}

// composeSpec POSTs an intent to the genui-1 endpoint. On success it returns
// the server-VALIDATED spec; on a 4xx it throws with the server's clean error
// (e.g. the validator rejected the composed widget) so the pane shows WHY
// without ever rendering unvalidated output. Exported for unit testing.
export async function composeSpec(intent) {
  const headers = { Accept: 'application/json', 'Content-Type': 'application/json' }
  const tok = authTokenSignal.value
  if (tok) headers['Authorization'] = 'Bearer ' + tok
  const res = await fetch('/api/command-center/genui/compose', {
    method: 'POST',
    headers,
    body: JSON.stringify({ intent }),
  })
  let body = null
  try { body = await res.json() } catch (_e) { /* non-JSON error */ }
  if (!res.ok) {
    const msg = (body && body.error) ? body.error : ('HTTP ' + res.status)
    throw new Error(msg)
  }
  return body // { spec, trace }
}

async function fetchSpec(id) {
  const headers = { Accept: 'application/json' }
  const tok = authTokenSignal.value
  if (tok) headers['Authorization'] = 'Bearer ' + tok
  const res = await fetch('/api/command-center/genui/spec/' + encodeURIComponent(id), { headers })
  if (!res.ok) throw new Error('HTTP ' + res.status)
  return res.json()
}

// bindData projects the live snapshot into the shape the spec refs expect.
// Every value here is non-secret (status, names, counts, decision questions) —
// the same projected slice the fixed Command Center already shows.
function bindData(snap) {
  if (!snap) return { totals: {}, conductors: [], sessions: [], decisionsWaiting: [], stuckSessions: [] }
  const conductors = Array.isArray(snap.conductors) ? snap.conductors : []
  const sessions = []
  const stuckSessions = []
  for (const cd of conductors) {
    for (const s of (cd.sessions || [])) {
      sessions.push(s)
      if (s.status === 'error' || s.status === 'stopped') stuckSessions.push(s)
    }
  }
  return {
    totals: snap.totals || {},
    conductors,
    sessions,
    stuckSessions,
    decisionsWaiting: Array.isArray(snap.decisionsWaiting) ? snap.decisionsWaiting : [],
  }
}

export function GenuiPane() {
  const snap = commandCenterSignal.value
  const conn = connectionSignal.value
  const [views, setViews] = useState([])
  const [activeId, setActiveId] = useState(null)
  const [spec, setSpec] = useState(null)
  const [err, setErr] = useState('')
  // genui-1 intent→compose state.
  const [intent, setIntent] = useState('')
  const [composing, setComposing] = useState(false)
  const [trace, setTrace] = useState(null) // {composer,tries,repaired} when the spec was composed

  // Load the view list once.
  useEffect(() => {
    fetchJSON('/api/command-center/genui/views')
      .then(d => {
        const vs = (d && Array.isArray(d.views)) ? d.views : []
        setViews(vs)
        if (vs.length && !activeId) setActiveId(vs[0].id)
      })
      .catch(e => setErr('Could not load views: ' + e.message))
  }, [])

  // Load the active hand-authored spec whenever the selection changes (RESHAPE).
  // A composed view sets activeId=null, so this effect no-ops until a view tab
  // is clicked again.
  useEffect(() => {
    if (!activeId) return
    setSpec(null)
    fetchSpec(activeId)
      .then(s => { setSpec(s); setErr(''); setTrace(null) })
      .catch(e => setErr('Could not load spec: ' + e.message))
  }, [activeId])

  // selectView switches back to a hand-authored view (clears any composed spec).
  function selectView(id) {
    setActiveId(id)
    setTrace(null)
    setErr('')
  }

  // runCompose sends the intent to the server composer. The returned spec is
  // already server-validated; a rejected/un-repairable compose throws with the
  // server's clean error, which we show instead of rendering anything.
  function runCompose() {
    const q = intent.trim()
    if (!q || composing) return
    setComposing(true)
    setErr('')
    composeSpec(q)
      .then(b => {
        setSpec(b.spec)
        setActiveId(null) // composed view is not one of the hand-authored tabs
        setTrace(b.trace || null)
      })
      .catch(e => { setErr('Could not compose that view: ' + e.message); setTrace(null) })
      .finally(() => setComposing(false))
  }

  const data = bindData(snap)

  return html`
    <div class="genui-pane" data-testid="genui-pane">
      <div class="genui-bar">
        <h1 class="genui-bar-title">Generative Command Center</h1>
        <span class=${`genui-live ${conn === 'connected' ? '' : 'stale'}`} data-testid="genui-live">
          ${conn === 'connected' ? '● live' : '● offline'}
        </span>
        <span class="genui-bar-hint">type an intent → the LLM emits a validated spec → it renders</span>
        <div class="genui-switch" data-testid="genui-switch">
          ${views.map(v => html`
            <button key=${v.id}
              class=${`genui-switch-btn ${v.id === activeId ? 'active' : ''}`}
              data-testid=${'genui-view-' + v.id}
              data-active=${v.id === activeId ? 'true' : 'false'}
              onClick=${() => selectView(v.id)}>${v.title}</button>
          `)}
        </div>
      </div>
      <form class="genui-intent" data-testid="genui-intent"
        onSubmit=${(e) => { e.preventDefault(); runCompose() }}>
        <input class="genui-intent-input" type="text"
          data-testid="genui-intent-input"
          placeholder="e.g. show me what's blocked · group everything by project · just the conductors"
          value=${intent}
          disabled=${composing}
          onInput=${(e) => setIntent(e.currentTarget.value)} />
        <button class="genui-intent-go" type="submit"
          data-testid="genui-intent-go"
          disabled=${composing || !intent.trim()}>
          ${composing ? 'Composing…' : 'Compose'}
        </button>
      </form>
      ${trace && html`
        <div class="genui-trace" data-testid="genui-trace">
          ✨ composed by <strong>${trace.composer}</strong> · ${trace.tries} ${trace.tries === 1 ? 'try' : 'tries'}${trace.repaired ? ' · repaired after validation' : ''}
        </div>`}
      <div class="genui-body" data-testid="genui-body">
        ${err && html`<div class="genui-error" data-testid="genui-load-error">⚠️ ${err}</div>`}
        ${!err && !spec && html`<div class="genui-loading" data-testid="genui-loading">Loading view…</div>`}
        ${!err && spec && renderSpec(spec, data)}
      </div>
    </div>
  `
}
