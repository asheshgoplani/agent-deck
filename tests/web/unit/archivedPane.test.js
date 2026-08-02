// Archiving tears down the tmux pane but never resets Status, so the wire value
// for an archived session is a stale last-known state — commonly 'error' (a
// vanished pane with no recoverable exit code classifies that way), which the
// status-dot CSS paints red. That made merely-archived sessions read as
// failures. The TUI masks this in connection_status.go
// (`if archived { icon, style = "■", SessionStatusStopped }`); projectArchived
// is the web-side equivalent.
//
// Tests projectArchived directly rather than rendering ArchivedPane. Importing
// the component works (the vitest alias order was fixed alongside this test),
// but rendering it still throws "Cannot read properties of undefined (reading
// '__$f')" from @preact/signals — the signals hook integration and the preact
// instance doing the rendering are not the same copy under this alias map.
// Fixing that is test-infra work beyond this bug; projectArchived is the whole
// of the behaviour under test, so assert it directly. If the render harness is
// repaired later, a render-level test asserting the .dot class would be a
// strictly better pin, since it would cover the Dot wiring too.
import { describe, expect, it } from 'vitest'

const archivedPaneModulePath = '../../../internal/web/static/app/panes/ArchivedPane.js'

describe('projectArchived status normalization', () => {
  it('rewrites a stale error status to stopped', async () => {
    const { projectArchived } = await import(archivedPaneModulePath)

    const row = projectArchived({
      id: 'a1',
      title: 'disk space',
      tool: 'claude',
      status: 'error',
      archivedAt: '2026-08-01T10:00:00Z',
    })

    expect(row.status).toBe('stopped')
    expect(row.title).toBe('disk space')
    expect(row.archivedAt).toBe('2026-08-01T10:00:00Z')
  })

  it('normalizes every stale status, not just error', async () => {
    const { projectArchived } = await import(archivedPaneModulePath)

    // A stale 'running' would otherwise render a pulsing green dot on a session
    // whose pane no longer exists.
    for (const status of ['running', 'waiting', 'idle', 'starting', 'queued', 'stopped', '']) {
      expect(projectArchived({ id: 'x', status }).status).toBe('stopped')
    }
  })

  it('tolerates a missing/undefined payload', async () => {
    const { projectArchived } = await import(archivedPaneModulePath)

    expect(projectArchived(undefined).status).toBe('stopped')
    expect(projectArchived({}).status).toBe('stopped')
  })
})
