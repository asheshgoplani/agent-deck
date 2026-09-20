// ConfirmDialog tone/label contract.
//
// The dialog started life as a delete-only modal (#519) and was later reused
// for archive, close and worktree-finish without being generalized, so every
// caller rendered a red "Delete" button — archiving a session offered to
// "Delete" it. The TUI is the reference: internal/ui/confirm_dialog.go paints
// ConfirmArchiveSession with ColorYellow and labels the button "Archive",
// while ConfirmDeleteSession stays ColorRed / "Delete".
//
// These assert the pure tone→presentation mapping. They deliberately do NOT
// render the component: every test under tests/web/unit/ covers exported pure
// functions, and adding the first render harness here would set test-infra
// precedent inside a focused bug fix. The rendered markup is pinned instead by
// session-actions-ui.spec.js ("Archive button: yellow …") and close-undo.spec.js,
// which assert the real DOM in a browser — that pair, not this file, is the
// regression guard for the reported bug.
import { describe, expect, it } from 'vitest'

const confirmDialogModulePath = '../../../internal/web/static/app/ConfirmDialog.js'

describe('confirm dialog tones', () => {
  it('maps the danger tone to the red button and kicker', async () => {
    const { CONFIRM_TONES } = await import(confirmDialogModulePath)

    expect(CONFIRM_TONES.danger.btnClass).toBe('btn danger')
    expect(CONFIRM_TONES.danger.kickerClass).toBe('kicker danger')
  })

  it('maps the warn tone to a distinct yellow button and kicker', async () => {
    const { CONFIRM_TONES } = await import(confirmDialogModulePath)

    expect(CONFIRM_TONES.warn.btnClass).toBe('btn warn')
    expect(CONFIRM_TONES.warn.kickerClass).toBe('kicker warn')
    // The whole point of the bug: archive must not reuse the delete styling.
    expect(CONFIRM_TONES.warn.btnClass).not.toBe(CONFIRM_TONES.danger.btnClass)
  })
})

describe('resolveConfirm', () => {
  it('defaults to the delete presentation when a caller passes nothing', async () => {
    const { resolveConfirm } = await import(confirmDialogModulePath)

    // Untouched call sites (session delete, archived-pane delete) must keep
    // rendering exactly what they render today — the e2e suites click a
    // button named "Delete" with class .btn.danger.
    const r = resolveConfirm({})
    expect(r.confirmLabel).toBe('Delete')
    expect(r.btnClass).toBe('btn danger')
    expect(r.kickerClass).toBe('kicker danger')
    expect(r.title).toBe('Are you sure?')
  })

  it('gives archive a yellow Archive button, mirroring the TUI', async () => {
    const { resolveConfirm } = await import(confirmDialogModulePath)

    const r = resolveConfirm({ tone: 'warn', confirmLabel: 'Archive', title: 'Archive session?' })
    expect(r.confirmLabel).toBe('Archive')
    expect(r.btnClass).toBe('btn warn')
    expect(r.kickerClass).toBe('kicker warn')
    expect(r.title).toBe('Archive session?')
  })

  it('falls back to the danger tone when handed an unknown tone', async () => {
    const { resolveConfirm } = await import(confirmDialogModulePath)

    // A typo in a call site should degrade to the safest-looking styling
    // rather than rendering an unstyled button.
    const r = resolveConfirm({ tone: 'chartreuse', confirmLabel: 'Go' })
    expect(r.btnClass).toBe('btn danger')
    expect(r.confirmLabel).toBe('Go')
  })
})
