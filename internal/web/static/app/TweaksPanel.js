// TweaksPanel.js -- Floating panel for accent / density / right-rail /
// notification toggles.
// Slides in over the bottom-right corner. Close with × or `?`.
import { html } from 'htm/preact'
import { Icon, ICONS } from './icons.js'
import {
  tweaksOpenSignal, accentSignal, densitySignal, railSignal,
} from './uiState.js'
import {
  pushConfigSignal, pushSubscribedSignal, pushBusySignal, pushPermissionSignal,
} from './state.js'
import { enablePush, disablePush } from './push.js'

const SWATCHES = [
  { id: 'blue',   color: 'var(--tn-blue)' },
  { id: 'amber',  color: 'var(--tn-yellow)' },
  { id: 'green',  color: 'var(--tn-green)' },
  { id: 'purple', color: 'var(--tn-purple)' },
]

export function TweaksPanel() {
  if (!tweaksOpenSignal.value) return null
  const accent = accentSignal.value
  const density = densitySignal.value
  const rail = railSignal.value
  const close = () => (tweaksOpenSignal.value = false)

  return html`
    <div class="tweaks" role="dialog" aria-label="Tweaks" data-testid="tweaks-panel">
      <div class="th">
        <${Icon} d=${ICONS.settings} size=${14}/>
        <div class="t">Tweaks</div>
        <button class="icon-btn" data-testid="tweaks-close" onClick=${close} aria-label="Close tweaks">
          <${Icon} d=${ICONS.x}/>
        </button>
      </div>
      <div class="tb">
        <div>
          <label>ACCENT</label>
          <div class="swatch-row">
            ${SWATCHES.map(s => html`
              <div key=${s.id}
                   data-testid=${`tweaks-accent-${s.id}`}
                   class=${`swatch ${accent === s.id ? 'on' : ''}`}
                   style=${{ background: s.color }}
                   onClick=${() => (accentSignal.value = s.id)}/>
            `)}
          </div>
        </div>
        <div>
          <label>DENSITY</label>
          <div class="seg-row">
            ${['compact','balanced','comfortable'].map(d => html`
              <button key=${d}
                      data-testid=${`tweaks-density-${d}`}
                      class=${`seg-btn ${density === d ? 'on' : ''}`}
                      onClick=${() => (densitySignal.value = d)}>${d}</button>
            `)}
          </div>
        </div>
        <div>
          <label>RIGHT RAIL</label>
          <div style="display: flex; align-items: center; gap: 8px;">
            <div class=${`switch ${rail === 'visible' ? 'on' : ''}`}
                 data-testid="tweaks-rail-switch"
                 onClick=${() => (railSignal.value = rail === 'visible' ? 'hidden' : 'visible')}/>
            <span style="font-family: var(--mono); font-size: 11px; color: var(--text-dim);">
              ${rail === 'visible' ? 'visible' : 'hidden'}
            </span>
          </div>
        </div>
        ${pushConfigSignal.value && html`<${NotificationsRow}/>`}
      </div>
    </div>
  `
}

// Rendered only when the server runs with --push and the browser supports the
// Push API (push.js leaves pushConfigSignal null otherwise).
function NotificationsRow() {
  const on = pushSubscribedSignal.value
  return html`
    <div>
      <label>NOTIFICATIONS</label>
      <div style="display: flex; align-items: center; gap: 8px;">
        <div class=${`switch ${on ? 'on' : ''}`}
             role="switch" aria-checked=${on} aria-label="Enable notifications"
             data-testid="tweaks-push-switch"
             onClick=${() => (on ? disablePush() : enablePush())}/>
        <span data-testid="tweaks-push-status" style="font-family: var(--mono); font-size: 11px; color: var(--text-dim);">
          ${pushStatusLabel(on, pushBusySignal.value, pushPermissionSignal.value === 'denied')}
        </span>
      </div>
    </div>
  `
}

function pushStatusLabel(on, busy, denied) {
  if (busy) return 'updating…'
  if (on) return 'on when this tab is unfocused'
  if (denied) return 'blocked in browser settings'
  return 'off'
}
