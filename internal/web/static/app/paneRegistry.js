// paneRegistry.js -- Single wiring table for the WebUI panes.
//
// PANES maps a tab id to { component, label, requiresCapability, props }.
// Insertion order is the tab-strip order in Topbar. AppShell iterates the
// same map, so adding or removing a pane means editing this file only.
//
// requiresCapability names a backend capability the pane depends on. When
// it is missing from the capability set the shell renders StubPane with the
// entry's `props` (title, message, hotkey) instead of the component.
import { TerminalPane } from './panes/TerminalPane.js'
import { CostsPane } from './panes/CostsPane.js'
import { FleetPane } from './panes/FleetPane.js'
import { CommandCenterPane } from './panes/CommandCenterPane.js'
import { ArchivedPane } from './panes/ArchivedPane.js'
import { StubPane } from './panes/StubPane.js'
import { SearchPane } from './panes/SearchPane.js'
import { McpPane } from './panes/McpPane.js'
import { SkillsPane } from './panes/SkillsPane.js'

// Capabilities the web API exposes today. Conductor topology and watcher
// event streams are TUI-only (see tests/web/PARITY_MATRIX.md), so they are
// absent here and their panes fall back to StubPane.
export const WEB_CAPABILITIES = new Set([
  'command-center', 'fleet', 'terminal', 'mcp', 'skills', 'costs', 'search', 'archived',
])

export const PANES = {
  'command-center': { component: CommandCenterPane, label: 'Command Center', requiresCapability: 'command-center' },
  fleet:     { component: FleetPane,    label: 'Fleet',    requiresCapability: 'fleet' },
  terminal:  { component: TerminalPane, label: 'Terminal', requiresCapability: 'terminal' },
  mcp:       { component: McpPane,      label: 'MCPs',     requiresCapability: 'mcp' },
  skills:    { component: SkillsPane,   label: 'Skills',   requiresCapability: 'skills' },
  conductor: {
    component: null, label: 'Conductor', requiresCapability: 'conductor',
    props: {
      title: 'Conductor',
      message: 'Conductor orchestration view is TUI-only. The web API does not expose child topology, bridges, or NEED escalation.',
    },
  },
  watchers: {
    component: null, label: 'Watchers', requiresCapability: 'watchers',
    props: {
      title: 'Watchers',
      message: 'Watcher framework events are routed in the backend; the web API does not surface event streams or routing config.',
    },
  },
  costs:     { component: CostsPane,    label: 'Costs',    requiresCapability: 'costs' },
  search:    { component: SearchPane,   label: 'Search',   requiresCapability: 'search' },
  archived:  { component: ArchivedPane, label: 'Archived', requiresCapability: 'archived' },
}

// Tab strip entries in display order.
export function tabStrip(panes = PANES) {
  return Object.entries(panes).map(([id, p]) => ({ id, label: p.label }))
}

// Resolve a tab id to the component and props the shell should mount.
// Returns null for unknown ids. An unmet capability yields StubPane.
export function resolvePane(id, capabilities = WEB_CAPABILITIES, panes = PANES) {
  const entry = panes[id]
  if (!entry) return null
  const met = !entry.requiresCapability || capabilities.has(entry.requiresCapability)
  if (met && entry.component) return { component: entry.component, props: entry.props || {} }
  return { component: StubPane, props: entry.props || { title: entry.label, message: '' } }
}
