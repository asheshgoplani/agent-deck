// ConnectionIndicator.js -- SSE/WS health indicator for topbar
import { html } from 'htm/preact'
import { connectionSignal } from './state.js'

const INDICATOR_CONFIG = {
  connected:    { color: 'bg-tn-green',                 label: 'connected' },
  connecting:   { color: 'bg-tn-yellow animate-pulse',  label: 'connecting' },
  reconnecting: { color: 'bg-tn-yellow animate-pulse',  label: 'reconnecting' },
  disconnected: { color: 'bg-tn-red',                   label: 'disconnected' },
  error:        { color: 'bg-tn-red',                   label: 'error' },
  idle:         { color: 'bg-tn-muted',                 label: 'idle' },
}

export function ConnectionIndicator() {
  const phase = connectionSignal.value
  const cfg = INDICATOR_CONFIG[phase] || INDICATOR_CONFIG.idle

  return html`
    <div class="flex items-center gap-1.5" title="SSE: ${phase}">
      <span class="w-2 h-2 rounded-full ${cfg.color}"></span>
      <span class="text-xs dark:text-tn-muted text-gray-500 hidden sm:inline">
        ${cfg.label}
      </span>
    </div>
  `
}
