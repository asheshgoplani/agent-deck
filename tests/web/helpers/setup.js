// helpers/setup.js -- Vitest setup file
// Polyfills + DOM matchers + per-test cleanup for Preact components.

import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/preact'
import { afterEach } from 'vitest'

afterEach(() => {
  cleanup()
})

// jsdom doesn't implement fetch — components that fetch on mount need it stubbed
// per-test. Provide a default that throws loudly so unmocked fetches are obvious.
if (typeof globalThis.fetch !== 'function') {
  globalThis.fetch = async (...args) => {
    throw new Error(
      `Unmocked fetch in unit test: ${JSON.stringify(args)}. ` +
      `Stub fetch with vi.stubGlobal('fetch', vi.fn(...)) in your test.`
    )
  }
}

// jsdom doesn't implement EventSource. Provide a minimal stub for components
// that open SSE streams on mount.
if (typeof globalThis.EventSource !== 'function') {
  globalThis.EventSource = class EventSource {
    constructor(url) {
      this.url = url
      this.readyState = 0
      this.listeners = new Map()
    }
    addEventListener(type, fn) {
      const arr = this.listeners.get(type) || []
      arr.push(fn)
      this.listeners.set(type, arr)
    }
    removeEventListener(type, fn) {
      const arr = this.listeners.get(type) || []
      this.listeners.set(type, arr.filter((f) => f !== fn))
    }
    close() {
      this.readyState = 2
    }
  }
}

// jsdom lacks ResizeObserver — needed by some layout-aware components.
if (typeof globalThis.ResizeObserver !== 'function') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// matchMedia for components that respond to viewport size in unit tests.
if (typeof globalThis.matchMedia !== 'function') {
  globalThis.matchMedia = (query) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return true },
  })
}
