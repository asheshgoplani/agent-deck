// panes/CommandCenterPane.js -- The Command Center: a live, two-way fleet
// god-view embedded in the agent-deck web UI (see
// conductor/agent-deck/COMMAND-CENTER-DESIGN.md). v1 productizes the hand-made
// status HTMLs:
//   - LIVE via the /events/command-center SSE feed (no polling) — conductor
//     lights + session lists update instantly.
//   - SEE-EVERYTHING: per-conductor status + active session lists (error/
//     stopped filtered OUT, the noise the user rejected), each showing what
//     it's working on. Honest-status v2 substates surfaced distinctly.
//   - DECISIONS WAITING ON YOU parsed from OPEN-ITEMS.md §D, with 💬 prefill.
//   - COMPLETION NOTIFICATIONS fire as toasts (wired in main.js).
//   - TWO-WAY input box routing to Maestro / a chosen conductor via the
//     supported `session send` primitive (POST /api/command-center/ask).
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { commandCenterSignal, connectionSignal, mutationsEnabledSignal } from '../state.js'
import { apiFetch } from '../api.js'
import { addToast } from '../Toast.js'

const STATUS_DOT = {
  running: '🟢',
  waiting: '🟡',
  idle: '⚪',
  error: '🔴',
  stopped: '⚫',
  absent: '⚫',
}

// Honest-status v2: surface model-unavailable / auth-401 distinctly rather than
// hiding them as plain "running".
const SUBSTATE_LABEL = {
  'model-unavailable': 'model unavailable',
  'auth-401': 'auth error (401)',
  'idle-at-empty-prompt': 'idle (empty prompt)',
}

function dotFor(name, status) {
  if (name === 'maestro' && status === 'running') return '🔵'
  return STATUS_DOT[status] || '⚪'
}

function liveCounts(counts) {
  if (!counts) return ''
  const order = ['running', 'waiting', 'idle']
  return order.filter(k => counts[k]).map(k => `${counts[k]} ${k}`).join(' · ')
}

function DecisionCard({ decision, onComment }) {
  return html`
    <div class="cc-ask">
      ${decision.id && html`<span class="cc-ask-id">${decision.id}</span>`}
      <span class="cc-ask-text">${decision.question}</span>
      <button class="cc-cmt" title="Comment / answer this"
        onClick=${() => onComment(decision)}>💬</button>
    </div>
  `
}

function SessionRow({ sess }) {
  const sub = sess.substate && SUBSTATE_LABEL[sess.substate]
  return html`
    <div class="cc-srow" data-testid="cc-session" data-status=${sess.status}>
      <span class="cc-sd">${STATUS_DOT[sess.status] || '⚪'}</span>
      <span class="cc-stt" title=${sess.workingOn || sess.title}>${sess.title}</span>
      ${sub && html`<span class="cc-sub" title=${'honest-status: ' + sess.substate}>${sub}</span>`}
    </div>
  `
}

function ConductorRow({ cd }) {
  const [open, setOpen] = useState(false)
  const sub = cd.substate && SUBSTATE_LABEL[cd.substate]
  return html`
    <div class=${`cc-cd ${open ? 'open' : ''}`} data-testid="cc-conductor" data-name=${cd.name}>
      <button class="cc-cd-head" onClick=${() => setOpen(o => !o)}>
        <span class="cc-dot">${dotFor(cd.name, cd.status)}</span>
        <span class="cc-nm">${cd.name}</span>
        <span class="cc-ac" title=${cd.currentlyWorkingOn || ''}>
          ${cd.currentlyWorkingOn || (cd.status === 'absent' ? 'no conductor session' : cd.status)}
          ${sub && html` · ${sub}`}
        </span>
        <span class="cc-lc">${liveCounts(cd.counts)}</span>
      </button>
      ${open && html`
        <div class="cc-cd-body">
          ${cd.sessions && cd.sessions.length
            ? cd.sessions.map(s => html`<${SessionRow} key=${s.id} sess=${s}/>`)
            : html`<div class="cc-sdone">no active sessions</div>`}
        </div>
      `}
    </div>
  `
}

export function CommandCenterPane() {
  const snap = commandCenterSignal.value
  const conn = connectionSignal.value
  const canMutate = mutationsEnabledSignal.value
  const [text, setText] = useState('')
  const [target, setTarget] = useState('maestro')
  const [sending, setSending] = useState(false)
  const [status, setStatus] = useState('ready')

  const onComment = (decision) => {
    const label = decision.id || decision.question.slice(0, 24)
    setText(`re ${label}: `)
    document.querySelector('.cc-input textarea')?.focus()
  }

  const send = async () => {
    const msg = text.trim()
    if (!msg || sending) return
    if (!canMutate) {
      addToast('Two-way input is disabled (web mutations off)', 'info')
      return
    }
    setSending(true); setStatus('sending…')
    try {
      const resp = await apiFetch('POST', '/api/command-center/ask', { target, text: msg })
      setStatus(`✓ routed to ${resp.routedTo || target}`)
      setText('')
    } catch (e) {
      setStatus('✗ ' + (e.message || 'send failed'))
    } finally {
      setSending(false)
    }
  }

  const onKey = (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      send()
    }
  }

  const targets = (snap && Array.isArray(snap.askTargets) ? snap.askTargets : ['maestro'])

  if (!snap) {
    return html`
      <div class="cc" data-testid="command-center-pane">
        <div class="cc-top">
          <h1>Command Center</h1>
          <span class=${`cc-live ${conn === 'connected' ? '' : 'stale'}`}>
            ${conn === 'connected' ? '● connecting…' : '● offline'}
          </span>
        </div>
        <div class="cc-empty" data-testid="cc-loading">Waiting for the first fleet snapshot…</div>
      </div>
    `
  }

  const decisions = Array.isArray(snap.decisionsWaiting) ? snap.decisionsWaiting : []
  const conductors = Array.isArray(snap.conductors) ? snap.conductors : []
  const totals = snap.totals || {}

  return html`
    <div class="cc" data-testid="command-center-pane">
      <div class="cc-top">
        <h1>Command Center</h1>
        <span class=${`cc-live ${conn === 'connected' ? '' : 'stale'}`} data-testid="cc-live">
          ${conn === 'connected' ? '● live' : '● offline'}
        </span>
        <span class="cc-totals" data-testid="cc-totals">
          ${totals.running || 0} running · ${totals.waiting || 0} waiting · ${totals.idle || 0} idle
        </span>
      </div>

      <div class="cc-cols">
        <div class="cc-col">
          <h2>👉 Needs you</h2>
          ${decisions.length
            ? decisions.map((d, i) => html`<${DecisionCard} key=${d.id || i} decision=${d} onComment=${onComment}/>`)
            : html`<div class="cc-sdone" data-testid="cc-no-decisions">nothing waiting on you 🎉</div>`}
        </div>

        <div class="cc-col">
          <h2>🛰️ The fleet — what each is doing</h2>
          ${conductors.length
            ? conductors.map(cd => html`<${ConductorRow} key=${cd.name} cd=${cd}/>`)
            : html`<div class="cc-sdone" data-testid="cc-no-conductors">no conductors detected</div>`}
        </div>
      </div>

      <div class="cc-input" data-testid="cc-input">
        <select value=${target} onChange=${e => setTarget(e.target.value)} title="Route to" data-testid="cc-target">
          ${targets.map(t => html`<option key=${t} value=${t === 'conductor-maestro' ? 'maestro' : t}>
            ${t === 'conductor-maestro' || t === 'maestro' ? 'Maestro (default)' : t}
          </option>`)}
        </select>
        <textarea
          placeholder="answer a decision, comment, or instruct… ⌘/Ctrl+Enter to send"
          value=${text}
          onInput=${e => setText(e.target.value)}
          onKeyDown=${onKey}></textarea>
        <button class="cc-send" disabled=${!text.trim() || sending} onClick=${send} data-testid="cc-send">➤ Send</button>
        <span class=${`cc-st ${status.startsWith('✓') ? 'ok' : status.startsWith('✗') ? 'err' : ''}`} data-testid="cc-status">${status}</span>
      </div>
    </div>
  `
}
