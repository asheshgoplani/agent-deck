import { expect, test } from '@playwright/test'

test('reads the timeline and returns to the same terminal', async ({ page, request }) => {
  await request.post('/__fixture/reset')
  await page.route('**/api/sessions/sess-001/timeline?*', route => {
    const first = new URL(route.request().url()).searchParams.get('tail') === '60'
    return route.fulfill({ json: {
      schema: 'agent-deck.recall.rows/v2',
      through_cursor: 'cursor',
      turns: first ? [
        { id: 'u', kind: 'user', body: 'Show the result' },
        { id: 'a', kind: 'assistant', body: '| Name | Result |\n| --- | --- |\n| Test | Pass |\n\n```js\nconst passed = true\n```' },
        { id: 't', kind: 'bash', title: 'Run tests', summary: 'Exit code 0', body: 'Tests passed' },
      ] : [],
    } })
  })
  let connections = 0
  let closed = false
  await page.routeWebSocket('**/ws/session/sess-001*', socket => {
    connections++
    socket.onClose(() => { closed = true })
  })
  await page.goto('/s/sess-001')
  const toggle = page.getByRole('group', { name: 'Session view' })
  const terminal = toggle.getByRole('button', { name: 'Terminal' })
  const chat = toggle.getByRole('button', { name: 'Chat' })
  await expect(terminal).toHaveAttribute('aria-pressed', 'true')
  const xterm = page.locator('.xterm-viewport')
  await expect(xterm).toBeVisible()
  const original = await xterm.elementHandle()
  await expect.poll(() => connections).toBe(1)
  await chat.click()
  await expect(chat).toHaveAttribute('aria-pressed', 'true')
  await expect(xterm).toBeHidden()
  const history = page.getByRole('region', { name: 'Chat history' })
  await expect(history.getByRole('cell', { name: 'Pass' })).toBeVisible()
  await expect(history.locator('pre code.language-js')).toHaveText('const passed = true')
  const detail = history.getByText('Tests passed', { exact: true })
  await expect(detail).toBeHidden()
  await history.getByText('Run tests · Exit code 0').click()
  await expect(detail).toBeVisible()
  await terminal.click()
  await expect(xterm).toBeVisible()
  expect(await xterm.evaluate((current, first) => current === first, original)).toBe(true)
  expect(connections).toBe(1)
  expect(closed).toBe(false)
  await chat.click()
  await page.reload()
  await expect(chat).toHaveAttribute('aria-pressed', 'true')
})
