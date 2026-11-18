import { expect, it } from 'vitest'
import { chatRows } from '../../../internal/web/static/app/chatRows.js'

it.each([
  ['user', 'user'], ['assistant', 'assistant'],
  ...['tool', 'bash', 'edit', 'read', 'subagent', 'todo', 'question', 'skill', 'command'].map(kind => [kind, 'tool']),
  ...['thinking', 'system', 'compaction', 'turn_end', 'future'].map(kind => [kind, 'note']),
])('maps %s to %s', (kind, type) => {
  const row = { id: 'stable', kind, title: 'Title', summary: 'Done', body: 'Body\nmore' }
  expect(chatRows([row])).toEqual([{ ...row, type }])
})

it('hides other rows even when they contain raw text', () => {
  expect(chatRows([{ kind: 'other', body: 'internal' }])).toEqual([])
})
