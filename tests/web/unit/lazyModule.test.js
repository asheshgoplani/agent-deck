// useLazyComponent is the one on-demand loader shared by AppShell (dialogs,
// group panel), the Fleet Status board and the Command Center markdown
// renderer. These pin its cache and retry contract.
import { describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'
import { waitFor } from '@testing-library/preact'
import { useLazyComponent } from '../../../internal/web/static/app/lazyModule.js'

function Probe({ needed, loader, cacheKey }) {
  const v = useLazyComponent(needed, loader, cacheKey, m => m.value)
  return html`<span>${v || 'none'}</span>`
}

const settle = () => new Promise(r => setTimeout(r, 30))

describe('useLazyComponent', () => {
  it('fetches once per key across mounts', async () => {
    let calls = 0
    const loader = () => { calls++; return Promise.resolve({ value: 'loaded' }) }
    const a = document.createElement('div')
    const b = document.createElement('div')
    render(html`<${Probe} needed=${true} loader=${loader} cacheKey="once"/>`, a)
    await waitFor(() => expect(a.textContent).toBe('loaded'))
    render(html`<${Probe} needed=${true} loader=${loader} cacheKey="once"/>`, b)
    await waitFor(() => expect(b.textContent).toBe('loaded'))
    expect(calls).toBe(1)
  })

  it('retries a failed fetch when `needed` turns true again', async () => {
    let calls = 0
    const loader = () => (++calls === 1 ? Promise.reject(new Error('offline')) : Promise.resolve({ value: 'loaded' }))
    const c = document.createElement('div')
    const mount = (needed) => render(html`<${Probe} needed=${needed} loader=${loader} cacheKey="retry"/>`, c)
    mount(true)
    await settle()
    expect([c.textContent, calls]).toEqual(['none', 1])
    mount(false)
    await settle()
    mount(true)
    await waitFor(() => expect(c.textContent).toBe('loaded'))
    expect(calls).toBe(2)
  })
})
