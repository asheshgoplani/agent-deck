// Sidebar.js -- Sidebar wrapper containing session list
import { html } from 'htm/preact'
import { SessionList } from './SessionList.js'

export function Sidebar() {
  return html`
    <div class="flex-1 overflow-y-auto">
      <${SessionList} />
    </div>
  `
}
