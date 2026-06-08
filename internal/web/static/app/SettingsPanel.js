// SettingsPanel.js -- Settings surface (reads GET /api/settings).
// Restyled (PR-B) to use the bundle's `.kv` row layout from app.css.
import { html } from 'htm/preact'
import { useState, useEffect } from 'preact/hooks'
import { settingsSignal } from './state.js'

export function SettingsPanel() {
  const [error, setError] = useState(null)
  const settings = settingsSignal.value

  useEffect(() => {
    if (settings) return
    fetch('/api/settings')
      .then(r => { if (!r.ok) throw new Error('Settings request failed: ' + r.status); return r.json() })
      .then(data => { settingsSignal.value = data })
      .catch(err => setError(err.message || 'Failed to load settings'))
  }, [])

  if (error) {
    return html`<div style="font-family: var(--mono); font-size: 12px; color: var(--tn-red);">${error}</div>`
  }
  if (!settings) {
    return html`<div style="font-family: var(--mono); font-size: 12px; color: var(--muted);">Loading…</div>`
  }
  return html`
    <div style="display: flex; flex-direction: column; gap: 2px;">
      <div class="kv"><span class="k">profile</span><span class="v">${settings.profile || 'default'}</span></div>
      <div class="kv"><span class="k">version</span><span class="v">${settings.version || 'unknown'}</span></div>
      <div class="kv"><span class="k">read-only</span><span class=${`v ${settings.readOnly ? 'warn' : 'ok'}`}>${settings.readOnly ? 'yes' : 'no'}</span></div>
      <div class="kv"><span class="k">web mutations</span><span class=${`v ${settings.webMutations ? 'ok' : 'warn'}`}>${settings.webMutations ? 'enabled' : 'disabled'}</span></div>
    </div>
  `
}
