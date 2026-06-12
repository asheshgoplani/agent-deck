// unit/toast.test.js -- pin the toast stack contract in Toast.js
// (addToast / removeToast + the toastsSignal / toastHistorySignal bridge).
//
// Behavior contract under test (verbatim from Toast.js source):
//   - addToast assigns monotonically increasing ids; type defaults to 'error'
//     when omitted/falsy; createdAt is stamped with Date.now().
//   - Visible stack capped at 3: pushing a 4th evicts the oldest NON-error
//     first (Array.findIndex over the post-push array, so a brand-new
//     non-error toast arriving while 3 errors are visible evicts ITSELF).
//   - All-errors eviction: when all visible toasts are errors and another
//     error arrives, the OLDEST error (index 0) is shifted off.
//   - Evicted and dismissed toasts are pushed to toastHistorySignal,
//     capped at 50 (slice(-50)) and persisted to localStorage key
//     `agentdeck_toast_history`.
//   - info/success auto-dismiss after exactly 5000ms; error toasts have no
//     timer and require explicit removeToast.
//
// Module note: Toast.js keeps `nextId` at module scope and Vitest caches the
// module, so ids increase across tests in this file — assertions are relative
// (strictly increasing / unique), never `id === 1`.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { addToast, removeToast } from '../../../internal/web/static/app/Toast.js'
import { toastsSignal, toastHistorySignal } from '../../../internal/web/static/app/state.js'

const HISTORY_KEY = 'agentdeck_toast_history'

describe('addToast / removeToast', () => {
  beforeEach(() => {
    toastsSignal.value = []
    toastHistorySignal.value = []
    localStorage.removeItem(HISTORY_KEY)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('basics', () => {
    it('assigns strictly increasing, unique ids', () => {
      addToast('first', 'info')
      addToast('second', 'info')
      addToast('third', 'info')
      const ids = toastsSignal.value.map(t => t.id)
      expect(new Set(ids).size).toBe(3)
      expect(ids[1]).toBeGreaterThan(ids[0])
      expect(ids[2]).toBeGreaterThan(ids[1])
    })

    it("defaults type to 'error' when omitted or falsy", () => {
      addToast('no type')
      addToast('empty type', '')
      expect(toastsSignal.value[0].type).toBe('error')
      expect(toastsSignal.value[1].type).toBe('error')
    })

    it('preserves explicit type and message, stamps createdAt', () => {
      const before = Date.now()
      addToast('saved ok', 'success')
      const t = toastsSignal.value[0]
      expect(t.message).toBe('saved ok')
      expect(t.type).toBe('success')
      expect(typeof t.createdAt).toBe('number')
      expect(t.createdAt).toBeGreaterThanOrEqual(before)
      expect(t.createdAt).toBeLessThanOrEqual(Date.now())
    })

    it('removeToast removes the matching toast and pushes it to history', () => {
      addToast('keep me', 'error')
      addToast('dismiss me', 'error')
      const target = toastsSignal.value.find(t => t.message === 'dismiss me')
      removeToast(target.id)
      expect(toastsSignal.value.map(t => t.message)).toEqual(['keep me'])
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['dismiss me'])
    })

    it('removeToast with an unknown id is a harmless no-op (no history push)', () => {
      addToast('still here', 'error')
      removeToast(-12345)
      expect(toastsSignal.value).toHaveLength(1)
      expect(toastHistorySignal.value).toHaveLength(0)
    })
  })

  describe('visible stack cap of 3', () => {
    it('evicts the oldest non-error first when a 4th toast arrives', () => {
      addToast('e1', 'error')
      addToast('i1', 'info')
      addToast('s1', 'success')
      addToast('e2', 'error')
      // Post-push array was [e1, i1, s1, e2]; findIndex(type !== 'error')
      // hits i1 (oldest non-error) → evicted to history.
      expect(toastsSignal.value.map(t => t.message)).toEqual(['e1', 's1', 'e2'])
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['i1'])
    })

    it('a NEW non-error toast arriving onto 3 errors evicts itself (source quirk)', () => {
      addToast('e1', 'error')
      addToast('e2', 'error')
      addToast('e3', 'error')
      addToast('i-new', 'info')
      // Post-push array [e1, e2, e3, i-new]: the only non-error is the new
      // toast itself, so it is evicted straight to history and never shown.
      expect(toastsSignal.value.map(t => t.message)).toEqual(['e1', 'e2', 'e3'])
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['i-new'])
    })

    it('all-errors eviction: a 4th error shifts off the OLDEST error', () => {
      addToast('e1', 'error')
      addToast('e2', 'error')
      addToast('e3', 'error')
      addToast('e4', 'error')
      expect(toastsSignal.value.map(t => t.message)).toEqual(['e2', 'e3', 'e4'])
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['e1'])
    })

    it('never shows more than 3 toasts regardless of how many are pushed', () => {
      for (let i = 0; i < 10; i++) addToast(`t${i}`, i % 2 === 0 ? 'info' : 'error')
      expect(toastsSignal.value.length).toBe(3)
      expect(toastHistorySignal.value.length).toBe(7)
    })
  })

  describe('auto-dismiss timers', () => {
    it('info auto-dismisses after exactly 5000ms (4999ms → still visible)', () => {
      vi.useFakeTimers()
      addToast('soon gone', 'info')
      vi.advanceTimersByTime(4999)
      expect(toastsSignal.value.map(t => t.message)).toContain('soon gone')
      vi.advanceTimersByTime(1)
      expect(toastsSignal.value).toHaveLength(0)
      // The expired toast lands in history like a manual dismissal.
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['soon gone'])
    })

    it('success auto-dismisses after 5000ms', () => {
      vi.useFakeTimers()
      addToast('done', 'success')
      vi.advanceTimersByTime(5000)
      expect(toastsSignal.value).toHaveLength(0)
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['done'])
    })

    it('error toasts have NO timer and persist until explicit dismissal', () => {
      vi.useFakeTimers()
      addToast('still broken', 'error')
      vi.advanceTimersByTime(60_000)
      expect(toastsSignal.value.map(t => t.message)).toEqual(['still broken'])
      expect(toastHistorySignal.value).toHaveLength(0)
      // Explicit dismissal is the only way out.
      removeToast(toastsSignal.value[0].id)
      expect(toastsSignal.value).toHaveLength(0)
      expect(toastHistorySignal.value.map(t => t.message)).toEqual(['still broken'])
    })
  })

  describe('toast history (cap 50 + localStorage persistence)', () => {
    it('persists dismissed toasts to localStorage key agentdeck_toast_history', () => {
      addToast('persist me', 'error')
      removeToast(toastsSignal.value[0].id)
      const stored = JSON.parse(localStorage.getItem(HISTORY_KEY))
      expect(Array.isArray(stored)).toBe(true)
      expect(stored).toHaveLength(1)
      expect(stored[0].message).toBe('persist me')
      expect(stored).toEqual(toastHistorySignal.value)
    })

    it('caps history at 50, dropping the oldest entries (slice(-50))', () => {
      // Prefill 49 synthetic entries, then dismiss 3 real toasts.
      toastHistorySignal.value = Array.from({ length: 49 }, (_, i) => ({
        id: -(i + 1), message: `old-${i}`, type: 'info', createdAt: 0,
      }))
      for (const m of ['new-a', 'new-b', 'new-c']) {
        addToast(m, 'error')
        removeToast(toastsSignal.value[0].id)
      }
      const history = toastHistorySignal.value
      expect(history).toHaveLength(50)
      // The two oldest prefilled entries fell off; newest are appended last.
      expect(history[0].message).toBe('old-2')
      expect(history.slice(-3).map(t => t.message)).toEqual(['new-a', 'new-b', 'new-c'])
      // localStorage mirrors the capped array.
      const stored = JSON.parse(localStorage.getItem(HISTORY_KEY))
      expect(stored).toHaveLength(50)
      expect(stored).toEqual(history)
    })

    it('appends history in dismissal order (newest last in the signal)', () => {
      addToast('first dismissed', 'error')
      addToast('second dismissed', 'error')
      const [a, b] = toastsSignal.value
      removeToast(a.id)
      removeToast(b.id)
      expect(toastHistorySignal.value.map(t => t.message))
        .toEqual(['first dismissed', 'second dismissed'])
    })
  })
})
