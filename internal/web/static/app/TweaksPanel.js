// TweaksPanel.js -- Floating panel for accent / density / right-rail toggles.
// Slides in over the bottom-right corner. Close with × or `?`.
import { html } from 'htm/preact'
import { Icon, ICONS } from './icons.js'
import {
  tweaksOpenSignal, accentSignal, densitySignal, railSignal,
} from './uiState.js'

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
    <div class="tweaks" role="dialog" aria-label="Tweaks">
      <div class="th">
        <${Icon} d=${ICONS.settings} size=${14}/>
        <div class="t">Tweaks</div>
        <button class="icon-btn" onClick=${close} aria-label="Close tweaks">
          <${Icon} d=${ICONS.x}/>
        </button>
      </div>
      <div class="tb">
        <div>
          <label>ACCENT</label>
          <div class="swatch-row">
            ${SWATCHES.map(s => html`
              <div key=${s.id}
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
                      class=${`seg-btn ${density === d ? 'on' : ''}`}
                      onClick=${() => (densitySignal.value = d)}>${d}</button>
            `)}
          </div>
        </div>
        <div>
          <label>RIGHT RAIL</label>
          <div style="display: flex; align-items: center; gap: 8px;">
            <div class=${`switch ${rail === 'visible' ? 'on' : ''}`}
                 onClick=${() => (railSignal.value = rail === 'visible' ? 'hidden' : 'visible')}/>
            <span style="font-family: var(--mono); font-size: 11px; color: var(--text-dim);">
              ${rail === 'visible' ? 'visible' : 'hidden'}
            </span>
          </div>
        </div>
      </div>
    </div>
  `
}
