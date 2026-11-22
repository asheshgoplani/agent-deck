// The first-paint module graph is under the total-byte-weight budget in
// .lighthouserc.json. xterm and its addons (~145 KB gzipped) are only needed
// once a session is selected, so nothing in the initial graph may import them
// statically.
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { resolve, join } from 'node:path'

const appDir = resolve(__dirname, '../../../internal/web/static/app')

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(e =>
    e.isDirectory() ? walk(join(dir, e.name)) : e.name.endsWith('.js') ? [join(dir, e.name)] : [])
}

describe('initial payload', () => {
  it('has no static import of xterm or its addons', () => {
    const offenders = walk(appDir).filter(f =>
      /^\s*import\s[^;]*?from\s*['"]@xterm\//m.test(readFileSync(f, 'utf8')))
    expect(offenders).toEqual([])
  })
})
