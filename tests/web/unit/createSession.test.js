// unit/createSession.test.js -- pins the POST /api/sessions payload built by
// CreateSessionDialog, including the groupPath added in the mobile overhaul.
import { describe, it, expect } from 'vitest'

// The dialog builds its payload inline; mirror that logic here as the
// contract. If the dialog's payload construction changes, update both.
function buildPayload({ title, tool, path, group, modelId }) {
  const payload = { title, tool, projectPath: path }
  if (group) payload.groupPath = group
  if (modelId) payload.modelId = modelId
  return payload
}

describe('create-session payload', () => {
  it('includes groupPath when a group is selected', () => {
    const p = buildPayload({ title: 't', tool: 'claude', path: '/p', group: 'work/sub' })
    expect(p).toEqual({ title: 't', tool: 'claude', projectPath: '/p', groupPath: 'work/sub' })
  })
  it('omits groupPath when no group is selected', () => {
    const p = buildPayload({ title: 't', tool: 'claude', path: '/p', group: '' })
    expect(p.groupPath).toBeUndefined()
  })
})
