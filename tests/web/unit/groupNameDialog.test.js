// unit/groupNameDialog.test.js -- pins groupRequestFor, the pure request-descriptor
// helper exported from GroupNameDialog.js.  We test the helper directly (no
// rendering) so the contract is locked independently of UI framework flakiness.
import { describe, it, expect } from 'vitest'
import { groupRequestFor } from '../../../internal/web/static/app/GroupNameDialog.js'

describe('groupRequestFor — create', () => {
  it('root create: POST /api/groups with {name} only', () => {
    const result = groupRequestFor('create', {}, 'my-group')
    expect(result).toEqual({ method: 'POST', path: '/api/groups', body: { name: 'my-group' } })
  })

  it('root create: omits parentPath when parentPath prop is empty string', () => {
    const result = groupRequestFor('create', { parentPath: '' }, 'my-group')
    // empty parentPath is falsy — must NOT be included in body
    expect(result.body).toEqual({ name: 'my-group' })
    expect(result.body).not.toHaveProperty('parentPath')
  })

  it('subgroup create: includes parentPath in body when non-empty parentPath prop', () => {
    const result = groupRequestFor('create', { parentPath: 'work' }, 'api')
    expect(result).toEqual({
      method: 'POST',
      path: '/api/groups',
      body: { name: 'api', parentPath: 'work' },
    })
  })

  it('subgroup create: nested parentPath is preserved verbatim', () => {
    const result = groupRequestFor('create', { parentPath: 'work/api' }, 'v2')
    expect(result.body).toEqual({ name: 'v2', parentPath: 'work/api' })
  })
})

describe('groupRequestFor — rename', () => {
  it('rename: PATCH /api/groups/{path} with {name}', () => {
    const result = groupRequestFor('rename', { groupPath: 'work' }, 'work-renamed')
    expect(result).toEqual({
      method: 'PATCH',
      path: '/api/groups/work',
      body: { name: 'work-renamed' },
    })
  })

  it('rename: encodes slashes in groupPath (encodeURIComponent)', () => {
    const result = groupRequestFor('rename', { groupPath: 'work/api' }, 'new-name')
    expect(result.path).toBe('/api/groups/work%2Fapi')
  })

  it('rename: encodes spaces and special chars in groupPath', () => {
    const result = groupRequestFor('rename', { groupPath: 'my group' }, 'new')
    expect(result.path).toBe('/api/groups/my%20group')
  })
})

describe('groupRequestFor — reparent', () => {
  it('reparent to another group: PATCH with {parentPath: selectedValue}', () => {
    const result = groupRequestFor('reparent', { groupPath: 'work' }, 'infra')
    expect(result).toEqual({
      method: 'PATCH',
      path: '/api/groups/work',
      body: { parentPath: 'infra' },
    })
  })

  it('reparent to root: includes parentPath: "" (move-to-root is a real operation)', () => {
    const result = groupRequestFor('reparent', { groupPath: 'work/api' }, '')
    expect(result).toEqual({
      method: 'PATCH',
      path: '/api/groups/work%2Fapi',
      body: { parentPath: '' },
    })
    // parentPath must be present even when empty — backend needs it to know "move to root"
    expect(result.body).toHaveProperty('parentPath')
  })

  it('reparent: encodes slashes in groupPath', () => {
    const result = groupRequestFor('reparent', { groupPath: 'work/api' }, 'infra')
    expect(result.path).toBe('/api/groups/work%2Fapi')
  })
})

describe('groupRequestFor — delete', () => {
  it('delete: DELETE /api/groups/{path} with no body', () => {
    const result = groupRequestFor('delete', { groupPath: 'work' }, '')
    expect(result).toEqual({ method: 'DELETE', path: '/api/groups/work' })
    expect(result.body).toBeUndefined()
  })

  it('delete: encodes slashes in groupPath', () => {
    const result = groupRequestFor('delete', { groupPath: 'work/api' }, '')
    expect(result.path).toBe('/api/groups/work%2Fapi')
    expect(result.method).toBe('DELETE')
  })

  it('delete: value parameter is ignored', () => {
    const r1 = groupRequestFor('delete', { groupPath: 'x' }, 'ignored')
    const r2 = groupRequestFor('delete', { groupPath: 'x' }, '')
    expect(r1.method).toBe('DELETE')
    expect(r1.path).toBe(r2.path)
  })
})

describe('groupRequestFor — path encoding edge cases', () => {
  it('single-segment path is passed through unmodified', () => {
    const result = groupRequestFor('rename', { groupPath: 'work' }, 'x')
    expect(result.path).toBe('/api/groups/work')
  })

  it('multi-segment path work/api encodes the slash to %2F', () => {
    const result = groupRequestFor('delete', { groupPath: 'work/api' }, '')
    expect(result.path).toBe('/api/groups/work%2Fapi')
  })

  it('deeper nesting a/b/c encodes all slashes', () => {
    const result = groupRequestFor('rename', { groupPath: 'a/b/c' }, 'x')
    expect(result.path).toBe('/api/groups/a%2Fb%2Fc')
  })
})
