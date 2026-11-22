import { html } from 'htm/preact'

const MAX_CHARS = 20000

const words = list => new Set(list.split(' '))

const C_LIKE = {
  comment: [/\/\/[^\n]*/y, /\/\*[\s\S]*?(?:\*\/|$)/y],
  string: [/"(?:\\.|[^"\\\n])*"?/y, /'(?:\\.|[^'\\\n])*'?/y, /`(?:\\.|[^`\\])*`?/y],
}
const HASH = {
  comment: [/#[^\n]*/y],
  string: [/"""[\s\S]*?(?:"""|$)/y, /'''[\s\S]*?(?:'''|$)/y, /"(?:\\.|[^"\\\n])*"?/y, /'(?:\\.|[^'\\\n])*'?/y],
}

const GRAMMARS = {
  go: { ...C_LIKE, keywords: words('break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota'), types: words('string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr byte rune bool error float32 float64 any complex64 complex128') },
  js: { ...C_LIKE, keywords: words('async await break case catch class const continue debugger default delete do else export extends finally for from function if import in instanceof let new of return static super switch this throw try typeof var void while with yield null undefined true false'), types: words('string number boolean object symbol bigint unknown never any void interface type enum implements readonly as') },
  py: { ...HASH, keywords: words('and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield None True False self'), types: words('int str float bool list dict set tuple bytes object') },
  sh: { ...HASH, string: HASH.string.slice(2), keywords: words('if then else elif fi for while until do done case esac in function return export local readonly set unset source echo cd exit') },
  rust: { ...C_LIKE, keywords: words('as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while'), types: words('i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 bool char str String Vec Option Result Box') },
  sql: { comment: [/--[^\n]*/y, /\/\*[\s\S]*?(?:\*\/|$)/y], string: [/'(?:''|[^'])*'?/y], keywords: words('select from where and or not insert into values update set delete create table index alter drop add column primary key foreign references join left right inner outer on group by order having limit offset as distinct null is in like between case when then else end default unique if exists returning begin commit') , ci: true },
  yaml: { ...HASH, string: HASH.string.slice(2), keywords: words('true false null yes no on off') },
  json: { string: [/"(?:\\.|[^"\\\n])*"?/y], keywords: words('true false null') },
}
const ALIASES = { golang: 'go', javascript: 'js', jsx: 'js', mjs: 'js', ts: 'js', tsx: 'js', typescript: 'js', python: 'py', bash: 'sh', shell: 'sh', zsh: 'sh', console: 'sh', rs: 'rust', yml: 'yaml', toml: 'yaml', jsonc: 'json', postgres: 'sql', postgresql: 'sql' }

const NUMBER = /(?:0[xX][0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?)\b/y
const WORD = /[A-Za-z_$][\w$]*/y

function match(patterns, text, at) {
  for (const re of patterns || []) {
    re.lastIndex = at
    const m = re.exec(text)
    if (m && m[0]) return m[0]
  }
  return null
}

function span(kind, text) {
  return html`<span class=${'tok-' + kind}>${text}</span>`
}

function highlightDiff(code) {
  return code.split('\n').flatMap((line, i) => {
    const kind = line.startsWith('+') && !line.startsWith('+++') ? 'add'
      : line.startsWith('-') && !line.startsWith('---') ? 'del'
      : line.startsWith('@@') ? 'meta' : null
    const out = kind ? span(kind, line) : line
    return i ? ['\n', out] : [out]
  })
}

export function highlightCode(code, language) {
  const lang = String(language || '').toLowerCase()
  if (!code || code.length > MAX_CHARS) return code
  if (lang === 'diff' || lang === 'patch') return highlightDiff(code)
  const grammar = GRAMMARS[ALIASES[lang] || lang]
  if (!grammar) return code
  const out = []
  let plain = ''
  const flush = () => { if (plain) { out.push(plain); plain = '' } }
  let i = 0
  while (i < code.length) {
    const ch = code[i]
    let tok = match(grammar.comment, code, i)
    if (tok) { flush(); out.push(span('com', tok)); i += tok.length; continue }
    tok = match(grammar.string, code, i)
    if (tok) { flush(); out.push(span('str', tok)); i += tok.length; continue }
    if (/[0-9]/.test(ch) && !/[\w$]/.test(code[i - 1] || '')) {
      tok = match([NUMBER], code, i)
      if (tok) { flush(); out.push(span('num', tok)); i += tok.length; continue }
    }
    if (/[A-Za-z_$]/.test(ch)) {
      tok = match([WORD], code, i)
      const key = grammar.ci ? tok.toLowerCase() : tok
      if (grammar.keywords?.has(key)) { flush(); out.push(span('kw', tok)) }
      else if (grammar.types?.has(key)) { flush(); out.push(span('type', tok)) }
      else if (code[i + tok.length] === '(') { flush(); out.push(span('fn', tok)) }
      else plain += tok
      i += tok.length
      continue
    }
    plain += ch
    i++
  }
  flush()
  return out
}
