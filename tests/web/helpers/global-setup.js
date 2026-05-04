// helpers/global-setup.js -- Playwright global setup.
//
// Builds the in-memory web fixture binary and spawns it on the port picked
// by scripts/run-e2e.mjs. Verifies the spawned PID and a one-shot startup
// token via /__fixture/whoami so the suite cannot false-pass against a
// stale server squatting on the chosen port.

import { spawn, execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..')
const FIXTURE_PKG = './tests/web/fixtures/cmd/web-fixture/'
const BIN_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture')
const PID_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture.pid')

export default async function globalSetup() {
  const port = process.env.AGENT_DECK_WEB_PORT
  const token = process.env.AGENT_DECK_FIXTURE_TOKEN
  if (!port || !token) {
    throw new Error(
      'global-setup: AGENT_DECK_WEB_PORT and AGENT_DECK_FIXTURE_TOKEN must be set. Run via `npm run test:e2e` (which calls scripts/run-e2e.mjs) instead of `npx playwright test` directly.',
    )
  }

  mkdirSync(dirname(BIN_PATH), { recursive: true })

  // Build the fixture binary. Pin Go 1.24 per the repo's CLAUDE.md mandate.
  console.log('[playwright] building web-fixture binary')
  execFileSync('go', ['build', '-o', BIN_PATH, FIXTURE_PKG], {
    cwd: REPO_ROOT,
    stdio: 'inherit',
    env: { ...process.env, GOTOOLCHAIN: 'go1.24.0' },
  })

  // Spawn the binary detached so we can kill it via PID file in teardown.
  console.log(`[playwright] starting web-fixture on 127.0.0.1:${port} (token=${token.slice(0, 8)}…)`)
  const proc = spawn(
    BIN_PATH,
    ['--listen', `127.0.0.1:${port}`, '--startup-token', token],
    {
      cwd: REPO_ROOT,
      stdio: ['ignore', 'inherit', 'inherit'],
      detached: true,
    },
  )
  proc.unref()
  writeFileSync(PID_PATH, String(proc.pid), 'utf8')

  // Wait for /healthz to be ready (max 10s).
  const deadline = Date.now() + 10_000
  let lastErr
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/healthz`)
      if (res.ok) {
        await verifyOurFixture(port, token, proc.pid)
        console.log('[playwright] web-fixture is healthy and verified')
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

async function verifyOurFixture(port, token, expectedPid) {
  // Hit the fixture's identity endpoint and check that we are talking to
  // the binary we just spawned. If a stale agent-deck server happened to be
  // bound to the picked port (extremely rare given the OS-allocated
  // ephemeral pick, but possible), it will either 404 here, return a
  // different PID, or — most importantly — not echo our random token.
  const res = await fetch(`http://127.0.0.1:${port}/__fixture/whoami`)
  if (!res.ok) {
    throw new Error(
      `web-fixture identity check failed: GET /__fixture/whoami returned ${res.status}. ` +
        `Some other server is bound to 127.0.0.1:${port} — kill it and retry.`,
    )
  }
  const body = await res.json()
  if (body.startupToken !== token) {
    throw new Error(
      `web-fixture identity check failed: startup token mismatch (expected ${token.slice(0, 8)}…, ` +
        `got ${String(body.startupToken).slice(0, 8)}…). ` +
        `A stale fixture is squatting on 127.0.0.1:${port}.`,
    )
  }
  if (body.pid !== expectedPid) {
    throw new Error(
      `web-fixture identity check failed: PID mismatch (spawned ${expectedPid}, /__fixture/whoami reports ${body.pid}). ` +
        `Test infra cannot guarantee it is talking to the binary it spawned.`,
    )
  }
}
