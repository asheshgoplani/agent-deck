import { describe, expect, it } from 'vitest'

const modPath = '../../../internal/web/static/app/timeFmt.js'

const MIN = 60 * 1000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const YEAR = 365 * DAY

// Canonical parity table — MUST stay byte-identical to the Go table in
// internal/ui/humanize_since_test.go. If these drift, the TUI and web show
// different strings for the same instant.
const cases = [
  ['zero', 0, 'just now'],
  ['45s', 45 * 1000, 'just now'],
  ['60s', 60 * 1000, '1m ago'],
  ['59m', 59 * MIN, '59m ago'],
  ['60m', 60 * MIN, '1h ago'],
  ['3h20m', 3 * HOUR + 20 * MIN, '3h 20m ago'],
  ['23h59m', 23 * HOUR + 59 * MIN, '23h 59m ago'],
  ['24h', 24 * HOUR, '1d ago'],
  ['2d5h', 2 * DAY + 5 * HOUR, '2d 5h ago'],
  ['6d23h', 6 * DAY + 23 * HOUR, '6d 23h ago'],
  ['7d', 7 * DAY, '1w ago'],
  ['3w2d', 23 * DAY, '3w 2d ago'],
  ['30d', 30 * DAY, '1mo ago'],
  ['5mo1w', 160 * DAY, '5mo 1w ago'],
  ['365d', 365 * DAY, '1y ago'],
  ['2y3mo', 820 * DAY, '2y 3mo ago'],
]

describe('humanizeSince — parity with Go humanize_since_test.go', () => {
  it.each(cases)('%s', async (_name, ms, want) => {
    const { humanizeSince } = await import(modPath)
    expect(humanizeSince(ms)).toBe(want)
  })

  it('drops a zero secondary component', async () => {
    const { humanizeSince } = await import(modPath)
    expect(humanizeSince(5 * HOUR)).toBe('5h ago')
  })
})

describe('formatRelativeTime', () => {
  it('returns em dash for empty / null / Go-zero timestamps', async () => {
    const { formatRelativeTime } = await import(modPath)
    expect(formatRelativeTime('')).toBe('—')
    expect(formatRelativeTime(null)).toBe('—')
    expect(formatRelativeTime('0001-01-01T00:00:00Z')).toBe('—')
  })

  it('formats a real ISO timestamp relative to a fixed now', async () => {
    const { formatRelativeTime } = await import(modPath)
    const now = Date.parse('2026-07-03T12:00:00Z')
    const iso = new Date(now - (3 * HOUR + 20 * MIN)).toISOString()
    expect(formatRelativeTime(iso, now)).toBe('3h 20m ago')
  })

  it('accepts numeric epoch zero', async () => {
    const { formatRelativeTime } = await import(modPath)
    expect(formatRelativeTime(0, YEAR)).toBe('1y ago')
  })

  it('treats future/skew as just now', async () => {
    const { formatRelativeTime } = await import(modPath)
    const now = Date.parse('2026-07-03T12:00:00Z')
    const iso = new Date(now + 5 * MIN).toISOString()
    expect(formatRelativeTime(iso, now)).toBe('just now')
  })
})
