// Pins the pane wiring table introduced for issue #2137. AppShell and Topbar
// both read PANES, so the tab strip order here is the order users see, and a
// pane whose backend capability is missing must fall back to StubPane with
// the same title/message it rendered before the registry existed.
import { describe, expect, it } from 'vitest'

const registryPath = '../../../internal/web/static/app/paneRegistry.js'
const stubPath = '../../../internal/web/static/app/panes/StubPane.js'

// Exactly the TABS list Topbar.js carried before the registry.
const EXPECTED_TAB_STRIP = [
  { id: 'command-center', label: 'Command Center' },
  { id: 'fleet',     label: 'Fleet'     },
  { id: 'terminal',  label: 'Terminal'  },
  { id: 'mcp',       label: 'MCPs'      },
  { id: 'skills',    label: 'Skills'    },
  { id: 'conductor', label: 'Conductor' },
  { id: 'watchers',  label: 'Watchers'  },
  { id: 'costs',     label: 'Costs'     },
  { id: 'search',    label: 'Search'    },
  { id: 'archived',  label: 'Archived'  },
]

describe('paneRegistry', () => {
  it('renders the same tab names in the same order as before', async () => {
    const { tabStrip } = await import(registryPath)
    expect(tabStrip()).toEqual(EXPECTED_TAB_STRIP)
    expect(tabStrip()).toMatchSnapshot()
  })

  it('resolves a pane with a met capability to its component', async () => {
    const { resolvePane, PANES } = await import(registryPath)
    const { StubPane } = await import(stubPath)
    for (const id of ['command-center', 'fleet', 'terminal', 'mcp', 'skills', 'costs', 'search', 'archived']) {
      const pane = resolvePane(id)
      expect(pane.component).toBe(PANES[id].component)
      expect(pane.component).not.toBe(StubPane)
      expect(pane.props).toEqual({})
    }
  })

  it('falls back to StubPane with the legacy props when a capability is unmet', async () => {
    const { resolvePane } = await import(registryPath)
    const { StubPane } = await import(stubPath)

    const conductor = resolvePane('conductor')
    expect(conductor.component).toBe(StubPane)
    expect(conductor.props).toEqual({
      title: 'Conductor',
      message: 'Conductor orchestration view is TUI-only. The web API does not expose child topology, bridges, or NEED escalation.',
    })

    const watchers = resolvePane('watchers')
    expect(watchers.component).toBe(StubPane)
    expect(watchers.props).toEqual({
      title: 'Watchers',
      message: 'Watcher framework events are routed in the backend; the web API does not surface event streams or routing config.',
    })
  })

  it('stubs a real pane when its capability is removed from the set', async () => {
    const { resolvePane, WEB_CAPABILITIES, PANES } = await import(registryPath)
    const { StubPane } = await import(stubPath)

    const without = new Set(WEB_CAPABILITIES)
    without.delete('costs')
    const pane = resolvePane('costs', without)
    expect(pane.component).toBe(StubPane)
    expect(pane.props).toEqual({ title: 'Costs', message: '' })
    expect(resolvePane('costs').component).toBe(PANES.costs.component)
  })

  it('returns null for an unknown tab id', async () => {
    const { resolvePane } = await import(registryPath)
    expect(resolvePane('nope')).toBe(null)
  })
})
