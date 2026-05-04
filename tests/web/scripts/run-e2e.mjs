// scripts/run-e2e.mjs -- bootstrap script for the Playwright e2e suite.
//
// Picks an OS-allocated ephemeral port and a random startup token BEFORE
// playwright.config.js is evaluated, so baseURL resolves to a port that is
// (a) almost certainly free at this instant and (b) cross-checked at
// runtime against the spawned fixture's identity.
//
// Without this wrapper, a fixed port (e.g. 38291) could be already held by
// a stale fixture from a crashed previous run, the conductor's actual web
// server, or any random local process — and the suite would silently run
// against that stale server, producing false-passes.
//
// The startup token closes the residual race: even if some process steals
// our picked port between pick and spawn, /__fixture/whoami returns a
// different (or empty) token and global-setup throws.

import net from 'node:net'
import { spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'

async function pickEphemeralPort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.unref()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address()
      srv.close((err) => (err ? reject(err) : resolve(port)))
    })
  })
}

const port = await pickEphemeralPort()
const token = randomBytes(16).toString('hex')

console.log(`[run-e2e] AGENT_DECK_WEB_PORT=${port} AGENT_DECK_FIXTURE_TOKEN=${token.slice(0, 8)}…`)

const result = spawnSync('npx', ['playwright', 'test', ...process.argv.slice(2)], {
  stdio: 'inherit',
  env: {
    ...process.env,
    AGENT_DECK_WEB_PORT: String(port),
    AGENT_DECK_FIXTURE_TOKEN: token,
  },
})
process.exit(result.status ?? 1)
