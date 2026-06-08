// helpers/setup.js -- Vitest setup file
// Polyfills + DOM matchers + per-test cleanup for Preact components.

import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/preact'
import { afterEach } from 'vitest'

afterEach(() => {
  cleanup()
})

// Some Node versions expose a disabled global localStorage unless
// --localstorage-file is provided. Components read the bare localStorage global
// at import time, so provide a deterministic in-memory implementation for unit
// tests before any component modules load.
if (!globalThis.localStorage) {
  const store = new Map()
  globalThis.localStorage = {
    getItem(key) {
      const k = String(key)
      return store.has(k) ? store.get(k) : null
    },
    setItem(key, value) {
      store.set(String(key), String(value))
    },
    removeItem(key) {
      store.delete(String(key))
    },
    clear() {
      store.clear()
    },
  }
}

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
