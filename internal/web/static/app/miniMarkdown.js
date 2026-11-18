// miniMarkdown.js -- shared renderer for Command Center and Fleet board
// summaries, and the chat view planned in #3124. It builds preact vnodes
// instead of an HTML string, so input text cannot inject markup: there is no
// innerHTML anywhere on this path.
//
// Supported: # through ###### headings, nested and mixed lists, tables with
// alignment, fenced code with highlighting, blockquotes, horizontal rules,
// **bold**, `code`, http(s) links and autolinks. Anything else renders as plain
// text. Nesting is capped at 16 levels, with deeper content kept as text or flat
// list items.
import { html } from 'htm/preact'
import { highlightCode } from './codeHighlight.js'

const MAX_NESTING = 16

const INLINE = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\)|<https?:\/\/[^\s<>]+>|https?:\/\/[^\s<>`]+)/gi

function link(href, label) {
  return html`<a href=${href} target="_blank" rel="noopener noreferrer">${label}</a>`
}

// Drops trailing punctuation, but keeps a ")" that closes a "(" in the URL.
function trimUrlTrail(url) {
  let open = 0
  let close = 0
  for (const char of url) { if (char === '(') open++; else if (char === ')') close++ }
  let end = url.length
  while (end > 0 && /[.,;:!?)\]'"]/.test(url[end - 1])) {
    if (url[end - 1] === ')') {
      if (open >= close) break
      close--
    }
    end--
  }
  return url.slice(0, end)
}

function inline(text) {
  const out = []
  let last = 0
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index))
    let tok = m[0]
    if (tok.startsWith('**')) {
      out.push(html`<strong>${inline(tok.slice(2, -2))}</strong>`)
    } else if (tok.startsWith('`')) {
      out.push(html`<code>${tok.slice(1, -1)}</code>`)
    } else if (tok.startsWith('<')) {
      out.push(link(tok.slice(1, -1), tok.slice(1, -1)))
    } else if (/^https?:/i.test(tok)) {
      tok = trimUrlTrail(tok)
      out.push(link(tok, tok))
    } else {
      const [, label, href] = tok.match(/^\[([^\]]+)\]\(([^)\s]+)\)$/)
      out.push(/^https?:\/\//i.test(href)
        ? link(href, label)
        : tok)
    }
    last = m.index + tok.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

function MarkdownCodeBlock({ code, language }) {
  const safeLanguage = /^[a-z0-9_+-]+$/i.test(language || '') ? language : ''
  return html`<pre class="markdown-code" tabIndex="0"><code class=${safeLanguage ? 'language-' + safeLanguage : ''}>${highlightCode(code, safeLanguage)}</code></pre>`
}

function splitTableRow(raw) {
  const cells = []
  let cell = ''
  let code = false
  const line = raw.trim()
  for (let i = 0; i < line.length; i++) {
    const char = line[i]
    if (char === '\\' && line[i + 1] === '|') { cell += '|'; i++; continue }
    if (char === '`') code = !code
    if (char === '|' && !code) { cells.push(cell.trim()); cell = '' }
    else cell += char
  }
  cells.push(cell.trim())
  if (cells[0] === '') cells.shift()
  if (cells[cells.length - 1] === '') cells.pop()
  return cells
}

function tableAlignment(raw) {
  const cells = splitTableRow(raw)
  if (!cells.length || !cells.every(cell => /^:?-{3,}:?$/.test(cell))) return null
  return cells.map(cell => cell.startsWith(':') && cell.endsWith(':') ? 'center' : cell.endsWith(':') ? 'right' : 'left')
}

// A line that starts another block ends a table even when it contains a pipe.
function startsBlock(raw) {
  return /^ {0,3}(?:#{1,6}\s|>|`{3,}|~{3,})/.test(raw) || !!listItem(raw) ||
    /^(?:-{3,}|\*{3,}|_{3,})$/.test(raw.replace(/\s/g, ''))
}

function listItem(raw) {
  const match = raw.match(/^( *)(?:[-+*]|(\d+)[.)])\s+(.*)$/)
  return match ? { indent: match[1].length, ordered: !!match[2], text: match[3] } : null
}

function parseList(lines, start, indent, depth) {
  const first = listItem(lines[start])
  const ordered = first.ordered
  const items = []
  let index = start
  while (index < lines.length) {
    const raw = lines[index]
    if (!raw.trim()) break
    const match = listItem(raw)
    if (match) {
      if (match.indent < indent) break
      if (match.indent > indent) {
        if (!items.length) break
        if (depth >= MAX_NESTING) {
          items.push({ text: match.text, children: [] })
          index++
          continue
        }
        const [child, next] = parseList(lines, index, match.indent, depth + 1)
        items[items.length - 1].children.push(child)
        index = next
        continue
      }
      if (match.ordered !== ordered) break
      items.push({ text: match.text, children: [] })
      index++
      continue
    }
    const continuation = raw.match(/^( +)\s*(.*)$/)
    if (items.length && continuation && continuation[1].length > indent && continuation[2]) {
      items[items.length - 1].text += ' ' + continuation[2]
      index++
      continue
    }
    break
  }
  const children = items.map(item => html`<li>${inline(item.text)}${item.children}</li>`)
  return [ordered ? html`<ol>${children}</ol>` : html`<ul>${children}</ul>`, index]
}

export function renderMarkdown(src, depth = 0) {
  if (depth >= MAX_NESTING) return [html`<p>${String(src || '')}</p>`]
  const lines = String(src || '').split(/\r?\n/)
  const blocks = []
  let para = []
  let fence = null
  let language = ''
  let code = []
  const flushPara = () => {
    if (para.length) blocks.push(html`<p>${inline(para.join(' '))}</p>`)
    para = []
  }
  const flushFence = () => {
    blocks.push(html`<${MarkdownCodeBlock} code=${code.join('\n')} language=${language}/>`)
    fence = null
    language = ''
    code = []
  }

  for (let index = 0; index < lines.length; index++) {
    const raw = lines[index]
    const line = raw.trim()
    const marker = line.match(/^(`{3,}|~{3,})(.*)$/)
    if (fence) {
      if (marker && marker[1][0] === fence[0] && marker[1].length >= fence.length && !marker[2].trim()) flushFence()
      else code.push(raw)
      continue
    }
    if (marker) {
      flushPara()
      fence = marker[1]
      language = marker[2].trim().split(/\s+/)[0] || ''
      continue
    }
    if (!line) { flushPara(); continue }

    if (index + 1 < lines.length) {
      const headers = splitTableRow(raw)
      const alignment = tableAlignment(lines[index + 1])
      const piped = raw.includes('|') && lines[index + 1].includes('|')
      if (alignment && (headers.length > 1 || piped) && headers.length === alignment.length) {
        flushPara()
        const rows = []
        index += 2
        while (index < lines.length && lines[index].trim() && lines[index].includes('|') && !startsBlock(lines[index])) {
          const cells = splitTableRow(lines[index])
          rows.push(headers.map((_, cell) => cells[cell] || ''))
          index++
        }
        index--
        blocks.push(html`<div class="markdown-table-scroll" role="region" aria-label="Scrollable table" tabIndex="0"><table>
          <thead><tr>${headers.map((header, cell) => html`<th style=${{ textAlign: alignment[cell] }}>${inline(header)}</th>`)}</tr></thead>
          <tbody>${rows.map(row => html`<tr>${row.map((value, cell) => html`<td style=${{ textAlign: alignment[cell] }}>${inline(value)}</td>`)}</tr>`)}</tbody>
        </table></div>`)
        continue
      }
    }

    const match = line.match(/^(#{1,6})\s+(.*)$/)
    if (match) {
      flushPara()
      const body = inline(match[2])
      const level = match[1].length
      blocks.push(level === 1 ? html`<h3>${body}</h3>` : level === 2 ? html`<h4>${body}</h4>` : level === 3 ? html`<h5>${body}</h5>` : html`<h6>${body}</h6>`)
      continue
    }
    if (/^(?:-{3,}|\*{3,}|_{3,})$/.test(line.replace(/\s/g, ''))) {
      flushPara()
      blocks.push(html`<hr/>`)
      continue
    }
    if (/^ {0,3}>/.test(raw)) {
      flushPara()
      const quoted = []
      while (index < lines.length && /^ {0,3}>/.test(lines[index])) {
        quoted.push(lines[index].replace(/^ {0,3}> ?/, ''))
        index++
      }
      index--
      blocks.push(html`<blockquote>${renderMarkdown(quoted.join('\n'), depth + 1)}</blockquote>`)
      continue
    }
    const item = listItem(raw)
    if (item) {
      flushPara()
      const [list, next] = parseList(lines, index, item.indent, depth + 1)
      blocks.push(list)
      index = next - 1
      continue
    }
    para.push(line)
  }
  if (fence) flushFence()
  flushPara()
  return blocks
}
