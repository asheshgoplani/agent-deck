// Sidebar.js -- Sidebar wrapper containing search filter + session list
import { html } from 'htm/preact'
import { SearchFilter } from './SearchFilter.js'
import { SessionList } from './SessionList.js'

export function Sidebar() {
  return html`
    <div class="flex flex-col flex-1 min-h-0">
      <${SearchFilter} />
      <div class="flex-1 overflow-y-auto">
        <${SessionList} />
      </div>
    </div>
  `
}
