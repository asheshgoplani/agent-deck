// miniMarkdown.js -- a deliberately small markdown renderer for conductor-
// written fleet summaries (Command Center). It builds preact vnodes instead of
// an HTML string, so annotation text can never inject markup: there is no
// innerHTML anywhere on this path.
//
// Supported: # / ## / ### headings, - / * / 1. list items, blank-line
// paragraphs, **bold**, `code`, and [text](http(s)://...) links. Anything else
// renders as plain text.
import { html } from 'htm/preact'

const INLINE = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g

function inline(text) {
  const out = []
  let last = 0
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const tok = m[0]
    if (tok.startsWith('**')) {
      out.push(html`<strong>${tok.slice(2, -2)}</strong>`)
    } else if (tok.startsWith('`')) {
      out.push(html`<code>${tok.slice(1, -1)}</code>`)
    } else {
      const [, label, href] = tok.match(/^\[([^\]]+)\]\(([^)\s]+)\)$/)
      out.push(/^https?:\/\//i.test(href)
        ? html`<a href=${href} target="_blank" rel="noopener noreferrer">${label}</a>`
        : tok)
    }
    last = m.index + tok.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

// renderMarkdown returns an array of block vnodes for `src`.
export function renderMarkdown(src) {
  const blocks = []
  let list = null // { ordered, items }
  let para = []
  const flushPara = () => {
    if (para.length) blocks.push(html`<p>${inline(para.join(' '))}</p>`)
    para = []
  }
  const flushList = () => {
    if (list) {
      const items = list.items.map(it => html`<li>${inline(it)}</li>`)
      blocks.push(list.ordered ? html`<ol>${items}</ol>` : html`<ul>${items}</ul>`)
    }
    list = null
  }
  for (const raw of String(src || '').split(/\r?\n/)) {
    const line = raw.trim()
    let m
    if (!line) { flushPara(); flushList(); continue }
    if ((m = line.match(/^(#{1,3})\s+(.*)$/))) {
      flushPara(); flushList()
      const body = inline(m[2])
      blocks.push(m[1].length === 1 ? html`<h3>${body}</h3>` : m[1].length === 2 ? html`<h4>${body}</h4>` : html`<h5>${body}</h5>`)
      continue
    }
    if ((m = line.match(/^(?:[-*]|(\d+)\.)\s+(.*)$/))) {
      flushPara()
      const ordered = !!m[1]
      if (!list || list.ordered !== ordered) { flushList(); list = { ordered, items: [] } }
      list.items.push(m[2])
      continue
    }
    flushList()
    para.push(line)
  }
  flushPara(); flushList()
  return blocks
}
