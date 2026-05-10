// e2e/parity-state.spec.js -- field-level parity assertions, derived from
// PARITY_MATRIX.md.
//
// The matrix is the single source of truth for which session fields the web
// JSON should expose. Anything in the matrix marked Present (web JSON cell
// is `MenuSession.<key>`) MUST appear in /api/sessions output. Anything
// marked MISSING MUST NOT appear until it is intentionally promoted in
// lockstep with the matrix.

import { test, expect } from '@playwright/test'
import { loadMatrix } from '../helpers/parity-matrix.js'

const MATRIX = loadMatrix()

// Pinned counts so silent row deletion fails the build.
const EXPECTED_STATE_ROWS = 45
const EXPECTED_PRESENT_FIELDS = 12

const PRESENT = MATRIX.stateFields.filter((f) => !f.isMissing && f.jsonKey)
const MISSING = MATRIX.stateFields.filter((f) => f.isMissing)

test.describe('parity: matrix structural invariants', () => {
  test('state-field row count matches expected (deletion guard)', () => {
    expect(
      MATRIX.stateFields.length,
      `PARITY_MATRIX.md state-field row count drifted (was ${EXPECTED_STATE_ROWS}, now ${MATRIX.stateFields.length}). Update EXPECTED_STATE_ROWS and add/remove tests in lockstep.`,
    ).toBe(EXPECTED_STATE_ROWS)
  })

  test('present-field count matches expected (extraction guard)', () => {
    expect(
      PRESENT.length,
      `Present-field count changed (was ${EXPECTED_PRESENT_FIELDS}, now ${PRESENT.length}) — confirm matrix rows still parse correctly and update the pin.`,
    ).toBe(EXPECTED_PRESENT_FIELDS)
  })
})

test.describe('parity: state fields surfaced by /api/sessions', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
    // Fork sess-001 so a session in the snapshot has parentSessionId set.
    // The matrix promises that field is present in the JSON shape; an empty
    // string would be omitted by Go's `omitempty` tag, so we need at least
    // one session that legitimately carries it.
    await request.post('/api/sessions/sess-001/fork')
  })

  // One test per PRESENT field — every matrix row gets a hit. The matrix
  // claim is about JSON shape (the field CAN be surfaced), not that every
  // session has a non-empty value, so we assert "at least one session in
  // the snapshot has the key".
  for (const field of PRESENT) {
    test(`field "${field.jsonKey}" is present on session JSON (matrix: ${field.field})`, async ({
      request,
    }) => {
      const res = await request.get('/api/sessions')
      expect(res.ok()).toBe(true)
      const body = await res.json()
      expect(body.sessions.length).toBeGreaterThan(0)
      const someoneHasIt = body.sessions.some((s) =>
        Object.prototype.hasOwnProperty.call(s, field.jsonKey),
      )
      expect(
        someoneHasIt,
        `expected at least one session JSON to carry MenuSession.${field.jsonKey} (matrix row "${field.field}"). ` +
          `Sessions seen: ${body.sessions.map((s) => s.id).join(', ')}.`,
      ).toBe(true)
    })
  }

  // One test per MISSING field — they must stay absent on every session
  // until promoted in lockstep with the matrix.
  for (const field of MISSING) {
    const candidateKeys = candidateJsonKeys(field.field)
    test(`field "${field.field}" stays MISSING from session JSON until matrix is updated`, async ({
      request,
    }) => {
      const res = await request.get('/api/sessions')
      const body = await res.json()
      expect(body.sessions.length).toBeGreaterThan(0)
      for (const s of body.sessions) {
        for (const key of candidateKeys) {
          expect(
            s[key],
            `session ${s.id} unexpectedly exposes "${key}" — promote PARITY_MATRIX.md row "${field.field}" out of MISSING in the same PR.`,
          ).toBeUndefined()
        }
      }
    })
  }
})

// Convert a matrix snake_case field name to plausible JSON key candidates.
// "loaded_mcp_names" → ["loadedMcpNames", "loaded_mcp_names"].
function candidateJsonKeys(snakeName) {
  const camel = snakeName.replace(/_([a-z0-9])/g, (_, c) => c.toUpperCase())
  return camel === snakeName ? [camel] : [camel, snakeName]
}
