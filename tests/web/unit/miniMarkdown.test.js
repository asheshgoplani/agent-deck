import { afterEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'
import { renderMarkdown } from '../../../internal/web/static/app/miniMarkdown.js'

let container
function mount(source) {
  container = document.createElement('div')
  document.body.appendChild(container)
  render(html`<div>${renderMarkdown(source)}</div>`, container)
  return container
}

afterEach(() => {
  if (container) { render(null, container); container.remove(); container = null }
})

describe('miniMarkdown conversation constructs', () => {
  it('renders GFM tables with alignment, escaped pipes, and pipes inside code', () => {
    const c = mount('| Name | Value | Note |\n| :--- | :---: | ---: |\n| A \\| B | `x|y` | **safe** |\n| second | 2 | done |')
    expect(c.querySelectorAll('thead th')).toHaveLength(3)
    expect(c.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(c.querySelector('tbody td').textContent).toBe('A | B')
    expect(c.querySelector('tbody code').textContent).toBe('x|y')
    expect(c.querySelector('tbody strong').textContent).toBe('safe')
    expect(c.querySelectorAll('th')[0].style.textAlign).toBe('left')
    expect(c.querySelectorAll('th')[1].style.textAlign).toBe('center')
    expect(c.querySelectorAll('th')[2].style.textAlign).toBe('right')
    expect(c.querySelector('.markdown-table-scroll')).not.toBeNull()
  })

  it('renders nested ordered and unordered lists', () => {
    const c = mount('- parent\n  - child\n    1. grandchild\n- sibling\n\n1. first\n   1. nested ordered\n2. second')
    const top = c.querySelector(':scope > div > ul')
    expect(top.querySelector(':scope > li > ul > li > ol > li').textContent).toBe('grandchild')
    expect([...top.children].filter(el => el.tagName === 'LI')).toHaveLength(2)
    const ordered = c.querySelector(':scope > div > ol')
    expect(ordered.querySelector(':scope > li > ol > li').textContent).toBe('nested ordered')
    expect([...ordered.children].filter(el => el.tagName === 'LI')).toHaveLength(2)
  })

  it('renders headings, nested blockquotes, inline code, links, and horizontal rules', () => {
    const c = mount('# One\n## Two\n### Three\n#### Four\n##### Five\n###### Six\n\n> quoted **text**\n> > nested quote\n> - quoted item\n\nUse `1 < 2` and [safe](https://example.test/a).\n\n---')
    expect([...c.querySelectorAll('h3,h4,h5,h6')].map(el => el.textContent)).toEqual(['One', 'Two', 'Three', 'Four', 'Five', 'Six'])
    expect(c.querySelector('blockquote blockquote').textContent).toBe('nested quote')
    expect(c.querySelector('blockquote li').textContent).toBe('quoted item')
    expect(c.querySelector('p code').textContent).toBe('1 < 2')
    expect(c.querySelector('p a').getAttribute('href')).toBe('https://example.test/a')
    expect(c.querySelector('p a').getAttribute('rel')).toContain('noreferrer')
    expect(c.querySelector('hr')).not.toBeNull()
  })

  it('preserves fenced code whitespace and escapes markup', () => {
    const c = mount('```html\n<img src=x onerror=alert(1)>\n  indented\n```')
    expect(c.querySelector('pre code').textContent).toBe('<img src=x onerror=alert(1)>\n  indented')
    expect(c.querySelector('pre code').className).toBe('language-html')
    expect(c.querySelector('img')).toBeNull()
  })

  it('keeps malicious markup and unsafe URLs as inert text in every block context', () => {
    const c = mount('<script>alert(1)</script>\n\n> <img src=x onerror=alert(1)>\n\n| HTML | Link |\n| --- | --- |\n| <svg onload=alert(1)> | [bad](javascript:alert(1)) |\n\n~~~xml\n<iframe srcdoc="<script>alert(1)</script>"></iframe>\n~~~')
    expect(c.querySelector('script,img,svg,iframe')).toBeNull()
    expect(c.querySelector('a')).toBeNull()
    expect(c.textContent).toContain('<script>alert(1)</script>')
    expect(c.textContent).toContain('<img src=x onerror=alert(1)>')
    expect(c.textContent).toContain('<svg onload=alert(1)>')
    expect(c.textContent).toContain('[bad](javascript:alert(1))')
    expect(c.textContent).toContain('<iframe srcdoc="<script>alert(1)</script>"></iframe>')
  })
})

describe('miniMarkdown list fences and numbering', () => {
  it.each([
    ['1.', '  ', '<ol><li>item more</li></ol>'],
    ['-', ' ', '<ul><li>item more</li></ul>'],
    ['10.', '   ', '<ol start="10"><li>item more</li></ol>'],
  ])('keeps adjacent plain text below the content column in a %s item', (marker, indent, expected) => {
    const c = mount(marker + ' item\n' + indent + 'more')
    expect(c.firstChild.innerHTML).toBe(expected)
  })

  it('renders a language fence as a code block inside the item', () => {
    const c = mount('- Run:\n  ```sh\n  make build\n    make ci\n  ```')
    const code = c.querySelector('li > pre.markdown-code > code')
    expect(code?.className).toBe('language-sh')
    expect(code?.textContent).toBe('make build\n  make ci')
  })

  it('renders a tilde fence without a language', () => {
    const c = mount('- Output:\n  ~~~\n  done\n  ~~~')
    const code = c.querySelector('li > pre.markdown-code > code')
    expect(code?.textContent).toBe('done')
    expect(code?.className).toBe('')
  })

  it('keeps blank lines before and inside the item fence', () => {
    const c = mount('- Run:\n\n  ```\n  first\n\n  second\n  ```')
    expect(c.querySelector('li > pre > code')?.textContent).toBe('first\n\nsecond')
  })

  it('keeps a blank line before an indented text continuation in the item', () => {
    const c = mount('10. Start\n\n    continued\n11. Finish')
    expect([...c.querySelectorAll('ol > li')].map(li => li.textContent)).toEqual(['Start continued', 'Finish'])
    expect(c.querySelectorAll('ol')).toHaveLength(1)
  })

  it('renders a fence inside a nested list item', () => {
    const c = mount('- Parent\n  4. Child\n     ```\n     child output\n     ```\n- Sibling')
    expect(c.querySelector('ul > li > ol > li > pre > code')?.textContent).toBe('child output')
    expect(c.querySelector('ul > li > ol').start).toBe(4)
  })

  it('ends an unterminated item fence at the next sibling or at EOF', () => {
    const c = mount('- Run:\n  ```\n  - code\n- Next:\n  ```\n  end')
    expect([...c.querySelectorAll('ul > li > pre > code')].map(code => code.textContent)).toEqual(['- code', 'end'])
  })

  it('continues the same ordered list after a fence and keeps text in order', () => {
    const c = mount('1. Run:\n\n   ```\n   make build\n   ```\n   Check the output\n2. Ship')
    const list = c.querySelector('ol')
    expect(c.querySelectorAll('ol')).toHaveLength(1)
    expect(list.children).toHaveLength(2)
    expect(list.firstElementChild.querySelector('pre').nextSibling.textContent).toBe('Check the output')
    expect(list.lastElementChild.textContent).toBe('Ship')
  })

  it.each([3, 0, 999999999])('sets the ordered list start to %i', start => {
    const c = mount(start + '. Step')
    expect(c.querySelector('ol').getAttribute('start')).toBe(String(start))
  })

  it('restores the default start when a rendered list changes to 1', () => {
    const c = mount('3. Third')
    expect(c.querySelector('ol').start).toBe(3)
    render(html`<div>${renderMarkdown('1. First')}</div>`, c)
    expect(c.querySelector('ol').getAttribute('start')).toBeNull()
    expect(c.querySelector('ol').start).toBe(1)
  })

  it('uses the first number of a new list after a top-level fence', () => {
    const c = mount('1. First\n```\noutput\n```\n3. Third')
    expect([...c.querySelectorAll('ol')].map(list => list.start)).toEqual([1, 3])
  })

  it('escapes source inside an item fence that opens on the marker line', () => {
    const source = '<img src=x onerror=alert(1)>'
    const c = mount('- ```html\n  ' + source + '\n  ```')
    expect(c.querySelector('li > pre > code')?.textContent).toBe(source)
    expect(c.querySelector('img')).toBeNull()
  })
})

describe('miniMarkdown links', () => {
  it('links bare and angle-bracket URLs without swallowing trailing punctuation', () => {
    const c = mount('Open https://example.com/a?b=1. Or <https://example.org/x>, then done')
    const links = [...c.querySelectorAll('a')]
    expect(links.map(a => a.getAttribute('href'))).toEqual(['https://example.com/a?b=1', 'https://example.org/x'])
    expect(links.every(a => a.target === '_blank' && a.rel === 'noopener noreferrer')).toBe(true)
    expect(c.textContent).toBe('Open https://example.com/a?b=1. Or https://example.org/x, then done')
  })

  it('links URLs inside bold text but not inside code', () => {
    const c = mount('**see https://example.com/p** and `https://example.com/code`')
    expect(c.querySelector('strong a').getAttribute('href')).toBe('https://example.com/p')
    expect(c.querySelector('code a')).toBeNull()
  })

  it('does not link non-http schemes', () => {
    const c = mount('javascript:alert(1) and [x](javascript:alert(1))')
    expect(c.querySelector('a')).toBeNull()
  })
})

describe('miniMarkdown code highlighting', () => {
  it('highlights keywords, strings, numbers and comments for a known language', () => {
    const c = mount('```go\n// load it\nfunc Load(n int) string { return "x" + 42 }\n```')
    const code = c.querySelector('pre code')
    expect(code.textContent).toBe('// load it\nfunc Load(n int) string { return "x" + 42 }')
    expect([...code.querySelectorAll('.tok-kw')].map(e => e.textContent)).toEqual(['func', 'return'])
    expect([...code.querySelectorAll('.tok-type')].map(e => e.textContent)).toEqual(['int', 'string'])
    expect(code.querySelector('.tok-fn').textContent).toBe('Load')
    expect(code.querySelector('.tok-str').textContent).toBe('"x"')
    expect(code.querySelector('.tok-num').textContent).toBe('42')
    expect(code.querySelector('.tok-com').textContent).toBe('// load it')
  })

  it('colors diff lines and leaves unknown languages as plain text', () => {
    const c = mount('```diff\n@@ -1 +1 @@\n-old\n+new\n```\n\n```brainfuck\nreturn "x"\n```')
    const [diff, plain] = c.querySelectorAll('pre code')
    expect(diff.querySelector('.tok-del').textContent).toBe('-old')
    expect(diff.querySelector('.tok-add').textContent).toBe('+new')
    expect(diff.textContent).toBe('@@ -1 +1 @@\n-old\n+new')
    expect(plain.querySelector('span')).toBeNull()
  })


})


describe('miniMarkdown edge cases and compatibility', () => {
  it('keeps the existing fleet summary fixture byte-for-byte compatible', () => {
    const c = mount('# Fleet\n- **2** need input\n- run `make ci`\n1. [PR](https://x.test/1)\n\nplain para')
    expect(c.firstChild.innerHTML).toBe('<h3>Fleet</h3><ul><li><strong>2</strong> need input</li><li>run <code>make ci</code></li></ul><ol><li><a href="https://x.test/1" target="_blank" rel="noopener noreferrer">PR</a></li></ol><p>plain para</p>')
  })

  it('keeps both callers heading and paragraph conventions', () => {
    const c = mount('## Today\n- 3 sessions need input\n\n### Next\n* review\n\nfirst line\nsecond line')
    expect(c.firstChild.innerHTML).toBe('<h4>Today</h4><ul><li>3 sessions need input</li></ul><h5>Next</h5><ul><li>review</li></ul><p>first line second line</p>')
  })

  it('renders mixed sibling lists and indented continuation lines', () => {
    const c = mount('- parent\n  continuation\n  1. ordered child\n  - unordered child\n1. next\n+ last')
    expect(c.firstChild.innerHTML).toBe('<ul><li>parent continuation<ol><li>ordered child</li></ol><ul><li>unordered child</li></ul></li></ul><ol><li>next</li></ol><ul><li>last</li></ul>')
  })

  it.each(['```', '~~~'])('renders %s fences without a language and closes an unterminated fence', fence => {
    const source = fence + '\n  <b> & \"quoted\"\n\nlast'
    const c = mount(source)
    expect(c.querySelector('pre code').textContent).toBe('  <b> & \"quoted\"\n\nlast')
    expect(c.querySelector('pre code').className).toBe('')
    expect(c.querySelector('pre code span,b')).toBeNull()
  })

  it('requires a matching fence at least as long and ignores nonempty closers', () => {
    const c = mount('````js\n```\n~~~~\n````no\nconst x = 1\n`````\nafter')
    expect(c.querySelector('pre code').textContent).toBe('```\n~~~~\n````no\nconst x = 1')
    expect(c.querySelector('p').textContent).toBe('after')
  })

  it('normalizes short and long table rows and rejects invalid separators', () => {
    const c = mount('A | B | C\n--- | --- | ---\nx | y\nx | y | z | ignored\n\nA | B\n-- | nope')
    expect([...c.querySelectorAll('tbody td')].map(el => el.textContent)).toEqual(['x', 'y', '', 'x', 'y', 'z'])
    expect(c.querySelector('p').textContent).toBe('A | B -- | nope')
  })

  it('keeps one-cell rows in a table and renders one-column tables', () => {
    const c = mount('A | B\n--- | ---\n| only |\nx | y\n\n| Status |\n| --- |\n| ready |\n\nplain\n---')
    const tables = c.querySelectorAll('table')
    expect(tables).toHaveLength(2)
    expect([...tables[0].querySelectorAll('tbody td')].map(el => el.textContent)).toEqual(['only', '', 'x', 'y'])
    expect([...tables[1].querySelectorAll('th, td')].map(el => el.textContent)).toEqual(['Status', 'ready'])
    expect(c.querySelector('hr')).not.toBeNull()
  })

  it('trims a long run of closing parens after a URL in linear time', () => {
    const started = performance.now()
    const c = mount('https://x.test/' + ')'.repeat(80000))
    expect(performance.now() - started).toBeLessThan(1000)
    expect(c.querySelector('a').getAttribute('href')).toBe('https://x.test/')
  })

  it('ends a table at a line that starts another block', () => {
    const c = mount('A | B\n--- | ---\nx | y\n## Next | Steps\n- a | b\n> q | r')
    expect([...c.querySelectorAll('tbody td')].map(el => el.textContent)).toEqual(['x', 'y'])
    expect(c.querySelector('h4').textContent).toBe('Next | Steps')
    expect(c.querySelector('li').textContent).toBe('a | b')
    expect(c.querySelector('blockquote').textContent).toContain('q | r')
  })

  it('keeps balanced parentheses in bare URLs and drops the unmatched one', () => {
    const c = mount('See https://example.test/Go_(lang) and (https://example.test/a_(b)).')
    expect([...c.querySelectorAll('a')].map(a => a.getAttribute('href'))).toEqual(['https://example.test/Go_(lang)', 'https://example.test/a_(b)'])
    expect(c.textContent).toBe('See https://example.test/Go_(lang) and (https://example.test/a_(b)).')
  })

  it.each(['javascript:alert', 'JaVaScRiPt:alert', 'data:text/html,x', 'vbscript:msgbox', 'file:///etc/passwd', '//example.test', 'javascript&#58;alert'])('rejects the unsafe link target %s', href => {
    const c = mount('[click](' + href + ')')
    expect(c.querySelector('a')).toBeNull()
    expect(c.textContent).toBe('[click](' + href + ')')
  })

  it('escapes link labels and prevents URL attribute injection', () => {
    const c = mount('[<img src=x onerror=alert>](https://example.test/\"onmouseover=alert)')
    const a = c.querySelector('a')
    expect(a.textContent).toBe('<img src=x onerror=alert>')
    expect(a.getAttribute('href')).toBe('https://example.test/\"onmouseover=alert')
    expect(a.hasAttribute('onmouseover')).toBe(false)
    expect(c.querySelector('img')).toBeNull()
  })

  it('rejects fence language attribute injection', () => {
    const c = mount('```js\"onmouseover=alert\n<img src=x onerror=alert>\n```')
    const code = c.querySelector('pre code')
    expect(code.className).toBe('')
    expect(code.hasAttribute('onmouseover')).toBe(false)
    expect(code.textContent).toBe('<img src=x onerror=alert>')
    expect(c.querySelector('img')).toBeNull()
  })

  it('escapes text in headings, lists, inline formatting and table headers and cells', () => {
    const attack = '<img src=x onerror=alert>'
    const c = mount('# ' + attack + '\n- **' + attack + '** `' + attack + '`\n\n| ' + attack + ' | B |\n| --- | --- |\n| &lt;script&gt; | ' + attack + ' |')
    expect(c.querySelector('img,script')).toBeNull()
    expect(c.querySelector('h3').textContent).toBe(attack)
    expect(c.querySelector('strong').textContent).toBe(attack)
    expect(c.querySelector('li code').textContent).toBe(attack)
    expect(c.querySelector('th').textContent).toBe(attack)
    expect(c.querySelector('td').textContent).toBe('&lt;script&gt;')
    expect(c.querySelectorAll('td')[1].textContent).toBe(attack)
  })

  it.each(['js', 'py', 'sh', 'rust', 'sql', 'yaml', 'json', 'typescript', 'patch', 'unknown', '__proto__', 'constructor'])('preserves and escapes all source text for %s', language => {
    const source = '// comment\n# comment\nSELECT true 42 0xff 1.2e3\nreturn \"<img src=x>\" & \'text\'\n+added\n-removed\n@@ section'
    const c = mount('```' + language + '\n' + source + '\n```')
    expect(c.querySelector('pre code').textContent).toBe(source)
    if (['unknown', '__proto__', 'constructor'].includes(language)) expect(c.querySelector('pre code span')).toBeNull()
    else expect(c.querySelector(language === 'patch' ? '.tok-add' : '.tok-kw')).not.toBeNull()
    expect(c.querySelector('img')).toBeNull()
  })

  it('leaves large code blocks plain to bound highlighting work', () => {
    const source = 'const x = 1\n'.repeat(2000)
    const c = mount('```js\n' + source + '```')
    expect(c.querySelector('pre code').textContent).toBe(source.slice(0, -1))
    expect(c.querySelector('pre code span')).toBeNull()
  })
})


describe('miniMarkdown nesting limits', () => {
  it('bounds a line of 20,000 blockquote markers and preserves the remaining text', () => {
    const source = '>'.repeat(20000) + ' <img src=x onerror=alert>'
    let c
    expect(() => { c = mount(source) }).not.toThrow()
    expect(c.querySelectorAll('blockquote')).toHaveLength(16)
    expect(c.textContent).toBe('>'.repeat(20000 - 16) + ' <img src=x onerror=alert>')
    expect(c.querySelector('img')).toBeNull()
  })

  it('flattens list nesting beyond 16 levels and preserves all 3,000 items', () => {
    const items = Array.from({ length: 3000 }, (_, i) => 'item-' + i)
    const source = items.map((item, i) => ' '.repeat(i) + '- ' + item).join('\n')
    let c
    expect(() => { c = mount(source) }).not.toThrow()
    expect(c.querySelectorAll('ul')).toHaveLength(16)
    expect(c.querySelectorAll('li')).toHaveLength(items.length)
    expect(c.textContent).toBe(items.join(''))
  })
})
