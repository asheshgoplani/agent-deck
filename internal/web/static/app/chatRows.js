const toolKinds = new Set(['tool', 'bash', 'edit', 'read', 'subagent', 'todo', 'question', 'skill', 'command'])

export function chatRows(rows) {
  return rows.filter(row => row.kind !== 'other').map(row => {
    let type = 'note'
    if (row.kind === 'user' || row.kind === 'assistant') type = row.kind
    else if (toolKinds.has(row.kind)) type = 'tool'
    return { ...row, type }
  })
}
