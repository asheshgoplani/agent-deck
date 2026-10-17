// Issue #2555: the web UI shipped a rename-group dialog (GroupNameDialog.js,
// mode 'rename') but nothing ever opened it. openRenameGroupDialog is the one
// entry point the `r` key and the group header button share.
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../internal/web/static/app/toasts.js', () => ({ addToast: vi.fn() }))

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const group = (name, path) => ({
  type: 'group',
  level: path.split('/').length - 1,
  group: { name, path, expanded: true, order: 0 },
})
const session = (id, groupPath) => ({
  type: 'session',
  level: groupPath.split('/').length,
  session: { id, title: id, groupPath, status: 'idle', tool: 'claude' },
})

async function reset({ mutations = true } = {}) {
  const state = await import(stateModulePath)
  const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
  state.sessionCostsSignal.value = {}
  sidebarFilterSignal.value = ''
  statusFiltersSignal.value = []
  groupExpandedSignal.value = {}
  state.mutationsEnabledSignal.value = mutations
  state.groupNameDialogSignal.value = null
  state.selectSession(null)
  state.sessionsSignal.value = [
    group('work', 'work'),
    group('innotrade', 'work/innotrade'),
    session('sess-1', 'work/innotrade'),
    // A session in a group the snapshot never sent: the client synthesizes
    // a placeholder row for it, which has no server record to rename.
    session('sess-2', 'ghost'),
  ]
  return state
}

describe('openRenameGroupDialog', () => {
  beforeEach(() => reset())

  it('opens the existing dialog in rename mode, prefilled with the group name', async () => {
    const { groupNameDialogSignal } = await reset()
    const { openRenameGroupDialog } = await import(dataModelModulePath)

    expect(openRenameGroupDialog('work/innotrade')).toBe(true)

    const d = groupNameDialogSignal.value
    expect(d).not.toBeNull()
    expect(d.mode).toBe('rename')
    expect(d.groupPath).toBe('work/innotrade')
    expect(d.currentName).toBe('innotrade')
  })

  it('follows the selection to the path the server reports after the rename', async () => {
    const { groupNameDialogSignal, selectedGroupSignal, selectGroup } = await reset()
    const { openRenameGroupDialog } = await import(dataModelModulePath)

    selectGroup('work/innotrade')
    openRenameGroupDialog('work/innotrade')
    groupNameDialogSignal.value.onSubmit({ path: 'work/innotrade', name: 'Inno Trade', newPath: 'work/Inno-Trade' })

    expect(selectedGroupSignal.value).toBe('work/Inno-Trade')
  })

  it('leaves an unrelated selection alone', async () => {
    const { groupNameDialogSignal, selectedGroupSignal, selectGroup } = await reset()
    const { openRenameGroupDialog } = await import(dataModelModulePath)

    selectGroup('work')
    openRenameGroupDialog('work/innotrade')
    groupNameDialogSignal.value.onSubmit({ newPath: 'work/Inno-Trade' })

    expect(selectedGroupSignal.value).toBe('work')
  })

  it('does nothing on a read-only server', async () => {
    const { groupNameDialogSignal } = await reset({ mutations: false })
    const { openRenameGroupDialog } = await import(dataModelModulePath)

    expect(openRenameGroupDialog('work')).toBe(false)
    expect(groupNameDialogSignal.value).toBeNull()
  })

  it('does nothing for a group the server never sent', async () => {
    const { groupNameDialogSignal } = await reset()
    const { openRenameGroupDialog, menuModelSignal } = await import(dataModelModulePath)

    expect(menuModelSignal.value.groups.find(g => g.path === 'ghost')?.derived).toBe(true)
    expect(openRenameGroupDialog('ghost')).toBe(false)
    expect(openRenameGroupDialog('no-such-group')).toBe(false)
    expect(groupNameDialogSignal.value).toBeNull()
  })
})

describe('groupApiUrl', () => {
  it('encodes each segment but keeps the separators', async () => {
    const { groupApiUrl } = await import(dataModelModulePath)
    expect(groupApiUrl('work/inno trade')).toBe('/api/groups/work/inno%20trade')
  })
})
