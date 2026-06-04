// unit/editSessionDialog.test.js -- pins diffUpdates, the pure diff function
// behind EditSessionDialog. The dialog sends ONLY changed fields; the backend
// clears AutoName whenever `title` is present, so it is CRITICAL that an
// unchanged title is never emitted (e.g. when the user only re-colors).
//
// Wire contract (matches internal/web/api_types.go UpdateSessionRequest):
// channels/extraArgs/plugins are comma/space text the server re-parses;
// skip/auto are bools; group + gemini fields ride the same PATCH.
import { describe, it, expect } from 'vitest'
import { diffUpdates } from '../../../internal/web/static/app/EditSessionDialog.js'

// join mirrors the dialog's joinList(', ') seeding so a pristine form diffs to {}.
const join = a => (Array.isArray(a) ? a.join(', ') : (a || ''))

// A representative original MenuSession. Mirrors the projected JSON shape:
// channels/extraArgs are []string; geminiYoloMode is a *bool -> bool|null.
const baseOriginal = {
  title: 'my-session',
  notes: 'hello',
  color: '#7aa2f7',
  tool: 'claude',
  channels: ['alerts', 'deploys'],
  extraArgs: ['--agent', 'reviewer'],
  skipPermissions: false,
  autoMode: false,
  groupPath: 'work',
  geminiModel: 'gemini-2.5-pro',
  geminiYoloMode: false,
}

// A form pre-filled to exactly match an original (the dialog's initial state).
function formFor(o) {
  return {
    title: o.title ?? '',
    notes: o.notes ?? '',
    color: o.color ?? '',
    tool: o.tool ?? 'claude',
    extraArgs: join(o.extraArgs),
    plugins: join(o.plugins),
    channels: join(o.channels),
    skipPermissions: !!o.skipPermissions,
    autoMode: !!o.autoMode,
    groupPath: o.groupPath ?? '',
    geminiModel: o.geminiModel ?? '',
    geminiYolo: o.geminiYoloMode === true,
  }
}

describe('diffUpdates', () => {
  it('returns {} when nothing changed', () => {
    expect(diffUpdates(formFor(baseOriginal), baseOriginal)).toEqual({})
  })

  it('handles a sparse original (absent omitempty fields) with empty form as {}', () => {
    const sparse = { title: 'quick', tool: 'claude' }
    expect(diffUpdates(formFor(sparse), sparse)).toEqual({})
  })

  it('emits ONLY color when only color changed — never title', () => {
    const form = { ...formFor(baseOriginal), color: '#ff0000' }
    const patch = diffUpdates(form, baseOriginal)
    expect(patch).toEqual({ color: '#ff0000' })
    expect(patch).not.toHaveProperty('title')
  })

  it('does not emit title for an auto-named (sparse) session that is only re-colored', () => {
    const auto = { title: 'auto-handle', tool: 'claude' }
    const form = { ...formFor(auto), color: '#abc123' }
    const patch = diffUpdates(form, auto)
    expect(patch).toEqual({ color: '#abc123' })
    expect(patch).not.toHaveProperty('title')
  })

  it('emits title when the title changed', () => {
    const form = { ...formFor(baseOriginal), title: 'renamed' }
    expect(diffUpdates(form, baseOriginal)).toEqual({ title: 'renamed' })
  })

  it('emits notes / groupPath when changed', () => {
    expect(diffUpdates({ ...formFor(baseOriginal), notes: 'bye' }, baseOriginal))
      .toEqual({ notes: 'bye' })
    expect(diffUpdates({ ...formFor(baseOriginal), groupPath: 'work/sub' }, baseOriginal))
      .toEqual({ groupPath: 'work/sub' })
  })

  it('emits tool when the tool changed', () => {
    expect(diffUpdates({ ...formFor(baseOriginal), tool: 'gemini' }, baseOriginal))
      // switching to gemini also stops diffing claude-only fields; tool is the
      // only field that differs here.
      .toEqual({ tool: 'gemini' })
  })

  describe('claude-only fields', () => {
    it('emits the raw channels text when changed (server re-parses)', () => {
      const original = { tool: 'claude', channels: ['a', 'b'] }
      const form = { ...formFor(original), channels: 'a, b ,c' }
      expect(diffUpdates(form, original)).toEqual({ channels: 'a, b ,c' })
    })

    it('omits channels when the joined text is unchanged', () => {
      const original = { tool: 'claude', channels: ['a', 'b'] }
      expect(diffUpdates(formFor(original), original)).toEqual({})
    })

    it('emits extraArgs text when changed', () => {
      const original = { tool: 'claude', extraArgs: ['--foo'] }
      const form = { ...formFor(original), extraArgs: '--foo --bar' }
      expect(diffUpdates(form, original)).toEqual({ extraArgs: '--foo --bar' })
    })

    it('emits skipPermissions / autoMode when toggled', () => {
      const original = { tool: 'claude', skipPermissions: false, autoMode: false }
      expect(diffUpdates({ ...formFor(original), skipPermissions: true }, original))
        .toEqual({ skipPermissions: true })
      expect(diffUpdates({ ...formFor(original), autoMode: true }, original))
        .toEqual({ autoMode: true })
    })

    it('ignores claude-only fields when tool is not claude', () => {
      const original = { tool: 'gemini', channels: ['a'], skipPermissions: false }
      // Form carries stale claude values, but tool!=='claude' so they are skipped.
      const form = { ...formFor(original), channels: 'changed', skipPermissions: true }
      expect(diffUpdates(form, original)).toEqual({})
    })
  })

  describe('gemini-only fields', () => {
    it('emits geminiModel when changed (tool gemini)', () => {
      const original = { tool: 'gemini', geminiModel: 'gemini-2.5-pro' }
      const form = { ...formFor(original), geminiModel: 'gemini-3.1-pro-preview' }
      expect(diffUpdates(form, original)).toEqual({ geminiModel: 'gemini-3.1-pro-preview' })
    })

    it('emits geminiYolo when toggled true (original null/absent)', () => {
      const original = { tool: 'gemini', geminiYoloMode: null }
      const form = { ...formFor(original), geminiYolo: true }
      expect(diffUpdates(form, original)).toEqual({ geminiYolo: true })
    })

    it('emits geminiYolo=false when toggled off from true', () => {
      const original = { tool: 'gemini', geminiYoloMode: true }
      const form = { ...formFor(original), geminiYolo: false }
      expect(diffUpdates(form, original)).toEqual({ geminiYolo: false })
    })

    it('ignores gemini fields when tool is not gemini', () => {
      const original = { tool: 'claude', geminiModel: 'gemini-2.5-pro' }
      const form = { ...formFor(original), geminiModel: 'changed', geminiYolo: true }
      expect(diffUpdates(form, original)).toEqual({})
    })
  })
})
