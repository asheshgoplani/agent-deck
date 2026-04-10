// SettingsPanel.js -- Settings surface (reads GET /api/settings)
// Displays server configuration: profile, version, readOnly, webMutations
import { html } from 'htm/preact'
import { useState, useEffect } from 'preact/hooks'
import { settingsSignal } from './state.js'

export function SettingsPanel() {
  const [error, setError] = useState(null)
  const settings = settingsSignal.value

  useEffect(() => {
    if (settings) return // already fetched

    fetch('/api/settings')
      .then(r => {
        if (!r.ok) throw new Error('Settings request failed: ' + r.status)
        return r.json()
      })
      .then(data => {
        settingsSignal.value = data
      })
      .catch(err => {
        setError(err.message || 'Failed to load settings')
      })
  }, [])

  if (error) {
    return html`<div class="text-xs dark:text-tn-red text-red-600">${error}</div>`
  }

  if (!settings) {
    return html`<div class="text-xs dark:text-tn-muted text-gray-600">Loading...</div>`
  }

  return html`<div class="space-y-1 text-xs">
    <div class="dark:text-tn-fg text-tn-light-fg">
      <span class="font-mono dark:text-tn-muted text-gray-500">Profile:</span> ${settings.profile || 'default'}
    </div>
    <div class="dark:text-tn-fg text-tn-light-fg">
      <span class="font-mono dark:text-tn-muted text-gray-500">Version:</span> ${settings.version || 'unknown'}
    </div>
    <div class="dark:text-tn-fg text-tn-light-fg">
      <span class="font-mono dark:text-tn-muted text-gray-500">Read-only:</span> ${settings.readOnly ? 'yes' : 'no'}
    </div>
    <div class="dark:text-tn-fg text-tn-light-fg">
      <span class="font-mono dark:text-tn-muted text-gray-500">Web mutations:</span> ${settings.webMutations ? 'enabled' : 'disabled'}
    </div>
  </div>`
}
