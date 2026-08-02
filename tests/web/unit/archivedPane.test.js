// Archiving tears down the tmux pane but never resets Status, so the wire value
// for an archived session is a stale last-known state — commonly 'error' (a
// vanished pane with no recoverable exit code classifies that way), which the
// status-dot CSS paints red. That made merely-archived sessions read as
// failures. The TUI masks this in connection_status.go
// (`if archived { icon, style = "■", SessionStatusStopped }`); projectArchived
// is the web-side equivalent.
//
// Tests projectArchived directly rather than rendering ArchivedPane: the vitest
// alias map cannot currently resolve preact/hooks for component sources, so no
// unit test renders a hooks-using component.
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
