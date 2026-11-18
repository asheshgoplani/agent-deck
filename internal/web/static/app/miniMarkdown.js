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

function fenceMarker(line) {
  const delimiter = line.match(/^(?:`{3,}|~{3,})/)?.[0]
  return delimiter ? { delimiter, info: line.slice(delimiter.length) } : null
}

function parseFence(lines, index, marker, indent = 0) {
  const { delimiter, info } = marker
  const language = info.trim().split(/\s+/)[0] || ''
  const prefix = ' '.repeat(indent)
  const code = []
  while (index < lines.length) {
    const raw = lines[index]
    const line = raw.trim()
    if (line && !raw.startsWith(prefix)) break
    index++
    const closer = fenceMarker(line)
    if (closer && closer.delimiter[0] === delimiter[0] && closer.delimiter.length >= delimiter.length && !closer.info) break
    code.push(raw.slice(indent))
  }
  return [html`<${MarkdownCodeBlock} code=${code.join('\n')} language=${language}/>`, index]
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
  const match = raw.match(/^( *)(?:[-+*]|(\d{1,9})[.)])\s+/)
  if (!match) return null
  const contentIndent = match[0].length
  return {
    indent: match[1].length,
    number: match[2] === undefined ? null : Number(match[2]),
    contentIndent,
    text: raw.slice(contentIndent),
  }
}

function appendItemBlock(item, block) {
  if (item.text) item.children.push(inline(item.text))
  item.children.push(block)
  item.text = ''
}

function parseList(lines, start, first, depth) {
  const { indent, number } = first
  const ordered = number !== null
  const items = []
  let item
  let index = start
  while (index < lines.length) {
    let raw = lines[index]
    if (!raw.trim()) {
      let next = index + 1
      while (next < lines.length && !lines[next].trim()) next++
      if (next === lines.length || lines[next].search(/[^ ]/) < item.contentIndent) break
      index = next
      raw = lines[index]
    }
    const match = listItem(raw)
    let text
    if (match) {
      if (match.indent < indent) break
      if (match.indent > indent && depth < MAX_NESTING) {
        const [child, next] = parseList(lines, index, match, depth + 1)
        appendItemBlock(item, child)
        index = next
        continue
      }
      if (match.indent === indent && (match.number !== null) !== ordered) break
      item = { text: '', contentIndent: match.contentIndent, children: [] }
      items.push(item)
      text = match.text
    } else {
      if (raw.search(/[^ ]/) < item.contentIndent) break
      text = raw.trimStart()
    }
    index++
    const marker = fenceMarker(text)
    if (marker) {
      const [block, next] = parseFence(lines, index, marker, item.contentIndent)
      appendItemBlock(item, block)
      index = next
    } else {
      item.text += item.text ? ' ' + text : text
    }
  }
  const children = items.map(item => html`<li>${item.children}${inline(item.text)}</li>`)
  // Uppercase makes Preact use the HTML attribute. Clearing the DOM start property would set it to 0.
  const list = ordered ? html`<ol START=${number === 1 ? undefined : number}>${children}</ol>` : html`<ul>${children}</ul>`
  return [list, index]
}

export function renderMarkdown(src, depth = 0) {
  if (depth >= MAX_NESTING) return [html`<p>${String(src || '')}</p>`]
  const lines = String(src || '').split(/\r?\n/)
  const blocks = []
  let para = []
  const flushPara = () => {
    if (para.length) blocks.push(html`<p>${inline(para.join(' '))}</p>`)
    para = []
  }

  for (let index = 0; index < lines.length; index++) {
    const raw = lines[index]
    const line = raw.trim()
    const marker = fenceMarker(line)
    if (marker) {
      flushPara()
      const [block, next] = parseFence(lines, index + 1, marker)
      blocks.push(block)
      index = next - 1
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
      const [list, next] = parseList(lines, index, item, depth + 1)
      blocks.push(list)
      index = next - 1
      continue
    }
    para.push(line)
  }
  flushPara()
  return blocks
}
