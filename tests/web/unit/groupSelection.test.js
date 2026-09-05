import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'

describe('selection mutual exclusion', () => {
  beforeEach(async () => {
    const { selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('selectGroup clears any selected session', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup } = await import(stateModulePath)

    selectedIdSignal.value = 'sess-001'
    selectGroup('work')

    expect(selectedGroupSignal.value).toBe('work')
    expect(selectedIdSignal.value).toBe(null)
  })

  it('selectSession clears any selected group', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup, selectSession } = await import(stateModulePath)

    selectGroup('work')
    selectSession('sess-002')

    expect(selectedIdSignal.value).toBe('sess-002')
    expect(selectedGroupSignal.value).toBe(null)
  })

  it('selectSession(null) clears both', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup, selectSession } = await import(stateModulePath)

    selectGroup('work')
    selectSession(null)

    expect(selectedIdSignal.value).toBe(null)
    expect(selectedGroupSignal.value).toBe(null)
  })
})
