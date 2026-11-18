package openpages

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"
)

// isStylesheet reports whether data really is a CSS stylesheet, so a file
// that is only NAMED .css (a renamed .env, a key, JSON, YAML, notes) is not
// inlined into a phone page (feature sweep D1 residual gap 1).
//
// The check is the CSS grammar itself, kept small and strict: the whole file
// (comments and strings aside) must be a sequence of
//
//   - at-rule statements ending in ";" (@import, @charset, @layer a, b; …),
//   - at-rules with a block (@media/@supports/@layer/@container/@keyframes …
//     hold rules; @font-face/@page and the rest hold declarations),
//   - qualified rules: a non-empty selector, then "{ declarations }", where
//     every declaration is "property: value" with a CSS property name
//     (an identifier or a --custom-property) and nested rules are allowed,
//
// with balanced braces, brackets and parentheses, closed strings and
// comments, and nothing left over. It must also do something: at least one
// declaration or one @import. Why this is sound for the job: secrets and
// config live in formats whose lines are KEY=VALUE, "key": value, key: value
// with no braces, [sections] or PEM armour; none of them parses as a run of
// selector { property: value } rules, so they are refused as a whole (one
// stray line refuses the file). Text that only LOOKS like CSS but declares
// nothing ("CANARY{}") is refused too: it styles nothing, so nothing is lost.
// What it cannot do: a secret deliberately written as a CSS value
// (a{content:"…"}) is a real stylesheet; a session that can write that could
// equally write the secret into the page itself. A stylesheet the parser does
// not understand is simply not inlined (the page shows unstyled), never sent.
func isStylesheet(data []byte) bool {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	c := &cssScan{s: bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))}
	return c.rules(false) && !c.bad && (c.decls > 0 || c.imports > 0)
}

type cssScan struct {
	s       []byte
	i       int
	decls   int
	imports int
	bad     bool
}

// cssProperty is a declaration's name: an identifier (optionally vendor- or
// hack-prefixed) or a custom property, then ":".
var cssProperty = regexp.MustCompile(`^\s*(--[A-Za-z0-9_-]*|[*_]?-?[A-Za-z_][A-Za-z0-9_-]*)\s*:`)

// groupAtRules hold rules (not declarations) in their block.
var groupAtRules = map[string]bool{"media": true, "supports": true, "layer": true, "container": true, "document": true,
	"-moz-document": true, "scope": true, "starting-style": true, "keyframes": true, "-webkit-keyframes": true,
	"-moz-keyframes": true, "-o-keyframes": true}

func (c *cssScan) eof() bool { return c.i >= len(c.s) }

// skip passes whitespace and comments; an unclosed comment is bad.
func (c *cssScan) skip() {
	for !c.eof() {
		switch {
		case c.s[c.i] == ' ' || c.s[c.i] == '\t' || c.s[c.i] == '\n' || c.s[c.i] == '\r' || c.s[c.i] == '\f':
			c.i++
		case bytes.HasPrefix(c.s[c.i:], []byte("/*")):
			end := bytes.Index(c.s[c.i+2:], []byte("*/"))
			if end < 0 {
				c.bad, c.i = true, len(c.s)
				return
			}
			c.i += 2 + end + 2
		default:
			return
		}
	}
}

// prelude reads up to the next ';', '{' or '}' outside strings, comments,
// parentheses and brackets, and returns the text and that byte (0 at EOF or
// on an unbalanced/unclosed construct).
func (c *cssScan) prelude() (string, byte) {
	start, depth := c.i, 0
	for !c.eof() {
		ch := c.s[c.i]
		switch {
		case ch == '\\':
			c.i += 2
			continue
		case ch == '"' || ch == '\'':
			c.i++
			for !c.eof() && c.s[c.i] != ch {
				if c.s[c.i] == '\\' {
					c.i++
				} else if c.s[c.i] == '\n' {
					return "", 0
				}
				c.i++
			}
			if c.eof() {
				return "", 0
			}
		case bytes.HasPrefix(c.s[c.i:], []byte("/*")):
			end := bytes.Index(c.s[c.i+2:], []byte("*/"))
			if end < 0 {
				c.i = len(c.s)
				return "", 0
			}
			c.i += 2 + end + 2
			continue
		case ch == '(' || ch == '[':
			depth++
		case ch == ')' || ch == ']':
			depth--
			if depth < 0 {
				return "", 0
			}
		case depth == 0 && (ch == ';' || ch == '{' || ch == '}'):
			return string(c.s[start:c.i]), ch
		}
		c.i++
	}
	return "", 0
}

// rules parses rules up to EOF (top level) or the closing '}' (nested).
func (c *cssScan) rules(nested bool) bool {
	for {
		c.skip()
		if c.bad {
			return false
		}
		if c.eof() {
			return !nested
		}
		switch c.s[c.i] {
		case '}':
			if !nested {
				return false
			}
			c.i++
			return true
		case '@':
			if !c.atRule() {
				return false
			}
		default:
			sel, term := c.prelude()
			if term != '{' || strings.TrimSpace(sel) == "" {
				return false
			}
			c.i++
			if !c.block() {
				return false
			}
		}
	}
}

// atRule parses one @rule, statement or block.
func (c *cssScan) atRule() bool {
	c.i++ // '@'
	start := c.i
	for !c.eof() && (c.s[c.i] == '-' || c.s[c.i] == '_' || c.s[c.i] >= 'a' && c.s[c.i] <= 'z' || c.s[c.i] >= 'A' && c.s[c.i] <= 'Z' || c.s[c.i] >= '0' && c.s[c.i] <= '9') {
		c.i++
	}
	name := strings.ToLower(string(c.s[start:c.i]))
	if name == "" {
		return false
	}
	_, term := c.prelude()
	switch term {
	case ';':
		c.i++
		if name == "import" {
			c.imports++
		}
		return true
	case '{':
		c.i++
		if groupAtRules[name] {
			return c.rules(true)
		}
		return c.block()
	}
	return false
}

// block parses declarations (and nested rules) up to the closing '}'.
func (c *cssScan) block() bool {
	for {
		c.skip()
		if c.bad || c.eof() {
			return false
		}
		switch c.s[c.i] {
		case '}':
			c.i++
			return true
		case ';':
			c.i++
			continue
		case '@':
			if !c.atRule() {
				return false
			}
			continue
		}
		item, term := c.prelude()
		switch term {
		case '{': // a nested rule (CSS nesting)
			if strings.TrimSpace(item) == "" {
				return false
			}
			c.i++
			if !c.block() {
				return false
			}
		case ';', '}':
			if !cssProperty.MatchString(item) {
				return false
			}
			c.decls++
			if term == ';' {
				c.i++
			}
		default:
			return false
		}
	}
}
