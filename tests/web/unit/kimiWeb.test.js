import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, waitFor } from '@testing-library/preact'
import { html } from 'htm/preact'

const apiPath = '../../../internal/web/static/app/api.js'
const statePath = '../../../internal/web/static/app/state.js'
const uiStatePath = '../../../internal/web/static/app/uiState.js'

vi.mock('../../../internal/web/static/app/api.js', () => ({
  apiFetch: vi.fn(),
  authHeaders: () => ({}),
}))

async function resetState() {
  const state = await import(statePath)
  const uiState = await import(uiStatePath)
  state.createSessionDialogSignal.value = null
  state.mutationsEnabledSignal.value = true
  state.pickerToolsSignal.value = []
  state.toolFilterFallbackSignal.value = false
  state.modelCatalogSignal.value = {}
  state.sessionCostsSignal.value = {}
  state.selectedIdSignal.value = null
  state.selectedGroupSignal.value = null
  uiState.sidebarFilterSignal.value = ''
  uiState.groupExpandedSignal.value = {}
  uiState.statusFiltersSignal.value = []
  uiState.showColsSignal.value = {}
  return state
}

describe('Kimi web session creation', () => {
  beforeEach(async () => {
    await resetState()
    const { apiFetch } = await import(apiPath)
    apiFetch.mockReset()
    apiFetch.mockImplementation(async (method, path) => {
      if (method === 'GET' && path.startsWith('/api/session-directory')) {
        return { missing: [] }
      }
      return { sessionId: 'kimi-id' }
    })
  })

  it('offers Kimi in the fallback picker and omits a permission override by default', async () => {
    const state = await import(statePath)
    state.createSessionDialogSignal.value = {
      groupPath: 'work', groupName: 'work', defaultPath: '/srv/kimi', tool: '', modelId: '',
    }
    const { CreateSessionDialog } = await import('../../../internal/web/static/app/CreateSessionDialog.js')
    const { container } = render(html`<${CreateSessionDialog}/>`)

    const kimiButton = [...container.querySelectorAll('.seg-btn')]
      .find(button => button.textContent === 'kimi')
    expect(kimiButton).toBeDefined()
    fireEvent.click(kimiButton)

    const modelSelect = container.querySelector('select')
    await waitFor(() => expect(
      [...modelSelect.options].some(option => option.value === 'kimi-code/kimi-for-coding'),
    ).toBe(true))
    fireEvent.input(modelSelect, { target: { value: 'kimi-code/kimi-for-coding' } })
    await waitFor(() => expect(modelSelect).toHaveValue('kimi-code/kimi-for-coding'))
    const titleInput = container.querySelector('input[placeholder="session-name"]')
    fireEvent.input(titleInput, { target: { value: 'kimi-web' } })
    await waitFor(() => expect(titleInput).toHaveValue('kimi-web'))
    fireEvent.submit(container.querySelector('form'))

    const { apiFetch } = await import(apiPath)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('POST', '/api/sessions', {
      title: 'kimi-web',
      tool: 'kimi',
      projectPath: '/srv/kimi',
      groupPath: 'work',
      modelId: 'kimi-code/kimi-for-coding',
    }))
  })

  it('submits Always Ask as an explicit Kimi permission override', async () => {
    const state = await import(statePath)
    state.createSessionDialogSignal.value = {
      groupPath: 'work', groupName: 'work', defaultPath: '/srv/kimi', tool: 'kimi', modelId: '',
    }
    const { CreateSessionDialog } = await import('../../../internal/web/static/app/CreateSessionDialog.js')
    const { container } = render(html`<${CreateSessionDialog}/>`)

    const permissionSelect = container.querySelector('select[aria-label="Kimi permission mode"]')
    expect(permissionSelect).not.toBeNull()
    fireEvent.input(permissionSelect, { target: { value: 'always-ask' } })
    await waitFor(() => expect(permissionSelect).toHaveValue('always-ask'))
    const titleInput = container.querySelector('input[placeholder="session-name"]')
    fireEvent.input(titleInput, { target: { value: 'kimi-always-ask' } })
    await waitFor(() => expect(titleInput).toHaveValue('kimi-always-ask'))
    fireEvent.submit(container.querySelector('form'))

    const { apiFetch } = await import(apiPath)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('POST', '/api/sessions', {
      title: 'kimi-always-ask',
      tool: 'kimi',
      projectPath: '/srv/kimi',
      groupPath: 'work',
      kimiPermissionMode: 'always-ask',
    }))
  })
})

describe('Kimi web session status', () => {
  it('renders a hook-driven waiting status and tool badge like other sessions', async () => {
    const state = await resetState()
    const { showColsSignal } = await import(uiStatePath)
    showColsSignal.value = { tool: true }
    state.sessionsSignal.value = [
      { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true } },
      {
        type: 'session', level: 1,
        session: {
          id: 'kimi-1', title: 'Kimi worker', groupPath: 'work', projectPath: '/srv/kimi',
          tool: 'kimi', status: 'waiting',
        },
      },
    ]
    const { Sidebar } = await import('../../../internal/web/static/app/Sidebar.js')
    const { container } = render(html`<${Sidebar}/>`)

    const row = container.querySelector('[data-row-key="s:kimi-1"]')
    expect(row.querySelector('.titleline .dot').classList.contains('waiting')).toBe(true)
    expect(row.querySelector('.titleline .tt').textContent).toBe('Kimi worker')
    expect(row.querySelector('.meta .tag').textContent).toBe('kimi')
    expect(row.querySelector('[data-testid="session-stop-btn"]')).not.toBeNull()
  })
})
