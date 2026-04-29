// helpers/global-setup.js -- Playwright global setup
//
// Builds the in-memory web fixture binary and spawns it on the configured
// port. The PID is written to a tempfile that global-teardown.js consumes.

import { spawn } from 'node:child_process'
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..')
const FIXTURE_PKG = './tests/web/fixtures/cmd/web-fixture/'
const BIN_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture')
const PID_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture.pid')
const PORT = process.env.AGENT_DECK_WEB_PORT || '38291'

export default async function globalSetup() {
  mkdirSync(dirname(BIN_PATH), { recursive: true })

  // Build the fixture binary. Pin Go 1.24 per the repo's CLAUDE.md mandate.
  console.log('[playwright] building web-fixture binary')
  execFileSync('go', ['build', '-o', BIN_PATH, FIXTURE_PKG], {
    cwd: REPO_ROOT,
    stdio: 'inherit',
    env: { ...process.env, GOTOOLCHAIN: 'go1.24.0' },
  })

  // Spawn the binary detached so we can kill it via PID file in teardown.
  console.log(`[playwright] starting web-fixture on 127.0.0.1:${PORT}`)
  const proc = spawn(BIN_PATH, ['--listen', `127.0.0.1:${PORT}`], {
    cwd: REPO_ROOT,
    stdio: ['ignore', 'inherit', 'inherit'],
    detached: true,
  })
  proc.unref()
  writeFileSync(PID_PATH, String(proc.pid), 'utf8')

  // Wait for /healthz to be ready (max 10s).
  const deadline = Date.now() + 10_000
  let lastErr
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/healthz`)
      if (res.ok) {
        console.log('[playwright] web-fixture is healthy')
        return
      }
      lastErr = new Error(`healthz returned ${res.status}`)
    } catch (err) {
      lastErr = err
    }
    await sleep(150)
  }
  throw new Error(`web-fixture failed to become healthy: ${lastErr?.message}`)
}
