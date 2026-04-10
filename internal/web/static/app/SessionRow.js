// SessionRow.js -- Single session item with status dot, title, tool badge, cost badge
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { selectedIdSignal, sessionCostsSignal, confirmDialogSignal, mutationsEnabledSignal } from './state.js'
import { apiFetch } from './api.js'
import { addToast } from './Toast.js'
import { memo } from './preactCompat.js'

const STATUS_COLORS = {
  running:  'bg-tn-green animate-pulse',
  waiting:  'bg-tn-yellow animate-pulse',
  idle:     'bg-tn-muted',
  error:    'bg-tn-red',
  starting: 'bg-tn-purple animate-pulse',
  stopped:  'bg-tn-muted/50',
}

function SessionRowImpl({ item, focused }) {
  const session = item.session
  const isSelected = selectedIdSignal.value === session.id
  const costUSD = sessionCostsSignal.value[session.id]
  const costLabel = (costUSD != null && costUSD >= 0.001)
    ? '$' + costUSD.toFixed(2)
    : null

  // Optimistic UI: override displayed status while mutation is in-flight
  const [optimisticStatus, setOptimisticStatus] = useState(null)
  const [mutating, setMutating] = useState(false)
  const displayStatus = optimisticStatus || session.status
  const dotColor = STATUS_COLORS[displayStatus] || 'bg-tn-muted'
  const [hovered, setHovered] = useState(false)
  // WEB-P0-4 prevention layer: read the mutations gate once per render.
  // When false (server webMutations=false), the write toolbar is hidden
  // and a read-only lock indicator is shown instead.
  const mutationsEnabled = mutationsEnabledSignal.value
  // Track focus-within via Preact state rather than CSS `group-focus-within`
  // because at equal-or-higher specificity the plain `.opacity-0` utility
  // still wins due to Tailwind v4 utility ordering quirks. Focus events
  // bubble from the inner action buttons up to this outer button, so a
  // single onFocus/onBlur pair suffices.
  const [hasFocusWithin, setHasFocusWithin] = useState(false)

  function handleClick() {
    selectedIdSignal.value = session.id
  }

  async function handleMutation(apiCall, pendingStatus) {
    setOptimisticStatus(pendingStatus)
    setMutating(true)
    try {
      await apiCall()
    } catch (err) {
      setOptimisticStatus(null)
      addToast(err.message || 'Action failed')
    } finally {
      setMutating(false)
      // SSE will deliver the real status; clear optimistic override after a short delay
      setTimeout(() => setOptimisticStatus(null), 3000)
    }
  }

  function handleStop(e) {
    e.stopPropagation()
    handleMutation(
      () => apiFetch('POST', '/api/sessions/' + session.id + '/stop'),
      'stopped'
    )
  }

  function handleRestart(e) {
    e.stopPropagation()
    handleMutation(
      () => apiFetch('POST', '/api/sessions/' + session.id + '/restart'),
      'starting'
    )
  }

  function handleDelete(e) {
    e.stopPropagation()
    confirmDialogSignal.value = {
      message: 'Delete session "' + (session.title || session.id) + '"? This cannot be undone.',
      onConfirm: () => apiFetch('DELETE', '/api/sessions/' + session.id)
    }
  }

  function handleFork(e) {
    e.stopPropagation()
    apiFetch('POST', '/api/sessions/' + session.id + '/fork')
  }

  return html`
    <li>
      <button
        type="button"
        onClick=${handleClick}
        onMouseEnter=${() => setHovered(true)}
        onMouseLeave=${() => setHovered(false)}
        onFocus=${() => setHasFocusWithin(true)}
        onBlur=${() => setHasFocusWithin(false)}
        class="group w-full min-w-0 relative flex items-center gap-sp-8 px-sp-12 py-1.5 min-h-[40px] leading-tight rounded text-left text-sm
          transition-colors border-l-4
          ${isSelected
            ? 'border-tn-blue dark:bg-tn-blue/30 bg-blue-100 dark:text-tn-fg text-gray-900 font-medium'
            : focused
              ? 'border-transparent dark:bg-tn-muted/10 bg-gray-100 dark:text-tn-fg text-gray-700'
              : 'border-transparent dark:hover:bg-tn-muted/10 hover:bg-gray-50 dark:text-tn-fg text-gray-700'
          }"
        style="padding-left: calc(${item.level || 0} * 1rem + 0.75rem)"
        data-session-id=${session.id}
        aria-current=${isSelected ? 'true' : 'false'}
      >
        <span class="w-2.5 h-2.5 rounded-full flex-shrink-0 ${dotColor}" title=${session.status}></span>
        <span class="flex-1 truncate min-w-0" title=${session.title || session.id}>${session.title || session.id}</span>
        <span class="text-xs dark:text-tn-muted text-gray-600 flex-shrink-0">
          ${session.tool || 'shell'}
        </span>
        ${costLabel && html`
          <span class="text-xs dark:text-tn-green text-green-700 flex-shrink-0 font-mono">
            ${costLabel}
          </span>
        `}
        ${!mutationsEnabledSignal.value && html`
          <span
            class="flex-shrink-0 dark:text-tn-muted/60 text-gray-600 ml-1"
            title="Server is in read-only mode (webMutations=false)"
            aria-label="Read-only"
          >
            <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"/>
            </svg>
          </span>
        `}
        ${mutationsEnabled && html`
          <div
            role="toolbar"
            aria-label="Session actions"
            onClick=${(e) => e.stopPropagation()}
            onMouseDown=${(e) => e.stopPropagation()}
            class="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-0.5
                   transition-opacity duration-[120ms] motion-reduce:transition-none
                   ${(hovered || focused || isSelected || hasFocusWithin) ? 'opacity-100 pointer-events-auto' : 'opacity-0 pointer-events-none'}
                   group-focus-within:opacity-100 group-focus-within:pointer-events-auto"
          >
            ${(displayStatus === 'running' || displayStatus === 'waiting') && html`
              <button type="button" onClick=${handleStop} disabled=${mutating} title="Stop (s)" aria-label="Stop session"
                class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                       dark:text-tn-muted hover:dark:text-tn-yellow hover:dark:bg-tn-yellow/10
                       text-gray-400 hover:text-yellow-600 hover:bg-yellow-50 transition-colors
                       focus-visible:opacity-100 focus-visible:pointer-events-auto
                       disabled:opacity-40 disabled:pointer-events-none">
                <svg class="w-3.5 h-3.5" fill="currentColor" viewBox="0 0 20 20">
                  <rect x="5" y="5" width="10" height="10" rx="1"/>
                </svg>
              </button>
            `}
            ${(displayStatus === 'idle' || displayStatus === 'stopped' || displayStatus === 'error') && html`
              <button type="button" onClick=${handleRestart} disabled=${mutating} title="Restart (r)" aria-label="Restart session"
                class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                       dark:text-tn-muted hover:dark:text-tn-green hover:dark:bg-tn-green/10
                       text-gray-400 hover:text-green-600 hover:bg-green-50 transition-colors
                       focus-visible:opacity-100 focus-visible:pointer-events-auto
                       disabled:opacity-40 disabled:pointer-events-none">
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                        d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/>
                </svg>
              </button>
            `}
            ${session.tool === 'claude' && html`
              <button type="button" onClick=${handleFork} title="Fork" aria-label="Fork session"
                class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                       dark:text-tn-muted hover:dark:text-tn-purple hover:dark:bg-tn-purple/10
                       text-gray-400 hover:text-purple-600 hover:bg-purple-50 transition-colors
                       focus-visible:opacity-100 focus-visible:pointer-events-auto">
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                        d="M8 7v8a2 2 0 002 2h6M8 7V5a2 2 0 012-2h4.586a1 1 0 01.707.293l4.414 4.414a1 1 0 01.293.707V15a2 2 0 01-2 2h-2"/>
                </svg>
              </button>
            `}
            <button type="button" onClick=${handleDelete} title="Delete (d)" aria-label="Delete session"
              class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                     dark:text-tn-muted hover:dark:text-tn-red hover:dark:bg-tn-red/10
                     text-gray-400 hover:text-red-600 hover:bg-red-50 transition-colors
                     focus-visible:opacity-100 focus-visible:pointer-events-auto">
              <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/>
              </svg>
            </button>
          </div>
        `}
      </button>
    </li>
  `
}

// PERF-G: wrap SessionRow in memo() so individual row rerenders are
// bailed out when their props didn't actually change. Without this, the
// sidebar re-renders every SessionRow on any parent update (cost update,
// selection change, SSE delta), which defeats the virtualization gains
// from plan 08-04. The comparator short-circuits on the props that
// actually drive the rendered output.
function areSessionRowPropsEqual(prev, next) {
  const prevS = prev.item && prev.item.session
  const nextS = next.item && next.item.session
  if (prevS === nextS) {
    return prev.focused === next.focused
  }
  if (!prevS || !nextS) return false
  return (
    prevS.id === nextS.id &&
    prevS.title === nextS.title &&
    prevS.status === nextS.status &&
    prevS.tool === nextS.tool &&
    prevS.groupPath === nextS.groupPath &&
    prev.focused === next.focused &&
    prev.item.level === next.item.level
  )
}

export const SessionRow = memo(SessionRowImpl, areSessionRowPropsEqual)
