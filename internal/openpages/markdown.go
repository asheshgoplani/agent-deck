package openpages

import (
	"bytes"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// RenderMarkdown turns Markdown into one self-contained HTML document sized
// for a phone: readable text, wide tables and code that scroll on their own,
// light and dark from prefers-color-scheme. Raw HTML in the source is shown
// as text, never interpreted, and links keep only http(s), mailto and
// relative targets. It covers the Markdown agents write: ATX and setext
// headings, paragraphs, emphasis, inline code, fenced and indented code,
// block quotes, ordered, unordered and task lists, pipe tables, rules,
// links, images and autolinks.
func RenderMarkdown(src []byte, title string) []byte {
	var body bytes.Buffer
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(src), "\r\n", "\n"), "\r", "\n"), "\n")
	renderBlocks(&body, lines)
	var out bytes.Buffer
	out.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("<meta name=\"color-scheme\" content=\"light dark\">\n")
	out.WriteString("<title>" + html.EscapeString(title) + "</title>\n<style>" + markdownCSS + "</style></head>\n")
	out.WriteString("<body><main class=\"md\">\n")
	out.Write(body.Bytes())
	out.WriteString("</main></body></html>\n")
	return out.Bytes()
}

const markdownCSS = `
:root{--bg:#ffffff;--fg:#1f2328;--muted:#59636e;--line:#d1d9e0;--code:#f6f8fa;--link:#0969da;--quote:#59636e}
@media (prefers-color-scheme:dark){:root{--bg:#0d1117;--fg:#e6edf3;--muted:#9198a1;--line:#3d444d;--code:#161b22;--link:#4493f8;--quote:#9198a1}}
html{-webkit-text-size-adjust:100%;text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:17px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Noto Sans",Helvetica,Arial,sans-serif;word-wrap:break-word;overflow-wrap:anywhere}
.md{max-width:860px;margin:0 auto;padding:16px 16px 48px}
h1,h2,h3,h4,h5,h6{line-height:1.25;margin:1.4em 0 .5em}
h1{font-size:1.75em;border-bottom:1px solid var(--line);padding-bottom:.3em}
h2{font-size:1.4em;border-bottom:1px solid var(--line);padding-bottom:.25em}
h3{font-size:1.2em}h4,h5,h6{font-size:1em}
p,ul,ol,blockquote,pre,.tbl{margin:0 0 1em}
a{color:var(--link)}
code{font:0.88em/1.4 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:var(--code);border-radius:4px;padding:.15em .35em}
pre{background:var(--code);border-radius:8px;padding:12px;overflow-x:auto;-webkit-overflow-scrolling:touch}
pre code{background:none;padding:0;font-size:0.85em;white-space:pre}
blockquote{border-left:4px solid var(--line);color:var(--quote);padding:0 1em;margin-left:0}
ul,ol{padding-left:1.6em}li{margin:.25em 0}
li.task{list-style:none;margin-left:-1.4em}
hr{border:0;border-top:1px solid var(--line);margin:1.5em 0}
img{max-width:100%;height:auto}
.tbl{overflow-x:auto;-webkit-overflow-scrolling:touch}
table{border-collapse:collapse;min-width:100%;font-size:.95em}
th,td{border:1px solid var(--line);padding:6px 10px;text-align:left;vertical-align:top;overflow-wrap:normal;word-break:normal}
th{background:var(--code)}
`

var (
	reATX      = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$`)
	reFence    = regexp.MustCompile("^ {0,3}(```+|~~~+)[ \t]*([^`\\s]*)")
	reHR       = regexp.MustCompile(`^ {0,3}((\*[ \t]*){3,}|(-[ \t]*){3,}|(_[ \t]*){3,})$`)
	reUL       = regexp.MustCompile(`^( {0,3})([-*+])[ \t]+(.*)$`)
	reOL       = regexp.MustCompile(`^( {0,3})(\d{1,9})[.)][ \t]+(.*)$`)
	reSetext1  = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)
	reSetext2  = regexp.MustCompile(`^ {0,3}-+[ \t]*$`)
	reTableSep = regexp.MustCompile(`^ *\|? *:?-+:? *(\| *:?-+:? *)*\|? *$`)
	reTask     = regexp.MustCompile(`^\[([ xX])\][ \t]+(.*)$`)
)

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func renderBlocks(out *bytes.Buffer, lines []string) {
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case isBlank(line):
			i++
		case reFence.MatchString(line):
			m := reFence.FindStringSubmatch(line)
			fence := m[1]
			lang := m[2]
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimLeft(lines[i], " "), fence[:3]) {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++
			}
			out.WriteString("<pre><code")
			if lang != "" {
				out.WriteString(` class="language-` + html.EscapeString(lang) + `"`)
			}
			out.WriteString(">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case reATX.MatchString(line):
			m := reATX.FindStringSubmatch(line)
			n := len(m[1])
			tag := "h" + strconv.Itoa(n)
			out.WriteString("<" + tag + ">" + inline(m[2]) + "</" + tag + ">\n")
			i++
		case reHR.MatchString(line):
			out.WriteString("<hr>\n")
			i++
		case strings.HasPrefix(strings.TrimLeft(line, " "), ">"):
			var inner []string
			for i < len(lines) && !isBlank(lines[i]) {
				l := strings.TrimLeft(lines[i], " ")
				if strings.HasPrefix(l, ">") {
					l = strings.TrimPrefix(strings.TrimPrefix(l, ">"), " ")
				}
				inner = append(inner, l)
				i++
			}
			out.WriteString("<blockquote>\n")
			renderBlocks(out, inner)
			out.WriteString("</blockquote>\n")
		case reUL.MatchString(line) || reOL.MatchString(line):
			i = renderList(out, lines, i)
		case strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t"):
			var code []string
			for i < len(lines) && (strings.HasPrefix(lines[i], "    ") || strings.HasPrefix(lines[i], "\t") || isBlank(lines[i])) {
				l := lines[i]
				if strings.HasPrefix(l, "\t") {
					l = l[1:]
				} else if len(l) >= 4 {
					l = l[4:]
				} else {
					l = ""
				}
				code = append(code, l)
				i++
			}
			for len(code) > 0 && code[len(code)-1] == "" {
				code = code[:len(code)-1]
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case strings.Contains(line, "|") && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			i = renderTable(out, lines, i)
		default:
			var para []string
			for i < len(lines) && !isBlank(lines[i]) {
				l := lines[i]
				if len(para) > 0 && (reATX.MatchString(l) || reFence.MatchString(l) || reHR.MatchString(l) && !reSetext2.MatchString(l) ||
					strings.HasPrefix(strings.TrimLeft(l, " "), ">") || reUL.MatchString(l) || reOL.MatchString(l)) {
					break
				}
				if len(para) > 0 && reSetext1.MatchString(l) {
					out.WriteString("<h1>" + inline(strings.Join(para, " ")) + "</h1>\n")
					para = nil
					i++
					break
				}
				if len(para) > 0 && reSetext2.MatchString(l) {
					out.WriteString("<h2>" + inline(strings.Join(para, " ")) + "</h2>\n")
					para = nil
					i++
					break
				}
				para = append(para, strings.TrimSpace(l))
				i++
			}
			if len(para) > 0 {
				out.WriteString("<p>" + inlineLines(para) + "</p>\n")
			}
		}
	}
}

// inlineLines joins paragraph lines; a line ending in two spaces or a
// backslash would be a hard break, but the trimmed lines keep it simple:
// lines are joined with a newline the browser folds into a space.
func inlineLines(para []string) string {
	parts := make([]string, len(para))
	for i, p := range para {
		parts[i] = inline(p)
	}
	return strings.Join(parts, "\n")
}

func renderList(out *bytes.Buffer, lines []string, i int) int {
	ordered := reOL.MatchString(lines[i]) && !reUL.MatchString(lines[i])
	tag := "ul"
	if ordered {
		tag = "ol"
		if m := reOL.FindStringSubmatch(lines[i]); m[2] != "1" {
			out.WriteString(`<ol start="` + html.EscapeString(strings.TrimLeft(m[2], "0")) + `">` + "\n")
		} else {
			out.WriteString("<ol>\n")
		}
	} else {
		out.WriteString("<ul>\n")
	}
	for i < len(lines) {
		var m []string
		if ordered {
			m = reOL.FindStringSubmatch(lines[i])
		} else {
			m = reUL.FindStringSubmatch(lines[i])
		}
		if m == nil {
			break
		}
		indent := len(m[1]) + len(m[2]) + 1
		if ordered {
			indent++
		}
		item := []string{m[3]}
		i++
		blankRun := 0
		for i < len(lines) {
			l := lines[i]
			if isBlank(l) {
				blankRun++
				item = append(item, "")
				i++
				continue
			}
			lead := len(l) - len(strings.TrimLeft(l, " \t"))
			if lead >= 2 {
				item = append(item, strings.TrimPrefix(l, strings.Repeat(" ", min(lead, indent))))
				blankRun = 0
				i++
				continue
			}
			if blankRun == 0 && !reUL.MatchString(l) && !reOL.MatchString(l) && !reATX.MatchString(l) && !reFence.MatchString(l) && !strings.HasPrefix(strings.TrimLeft(l, " "), ">") {
				item = append(item, l) // lazy continuation
				i++
				continue
			}
			break
		}
		for len(item) > 0 && item[len(item)-1] == "" {
			item = item[:len(item)-1]
		}
		class := ""
		if !ordered {
			if t := reTask.FindStringSubmatch(item[0]); t != nil {
				box := `<input type="checkbox" disabled> `
				if t[1] != " " {
					box = `<input type="checkbox" checked disabled> `
				}
				item[0] = t[2]
				class = ` class="task"`
				out.WriteString("<li" + class + ">" + box)
				writeItem(out, item)
				out.WriteString("</li>\n")
				if blankRun > 0 && i < len(lines) && (reUL.MatchString(lines[i]) == !ordered) {
					continue
				}
				continue
			}
		}
		out.WriteString("<li>")
		writeItem(out, item)
		out.WriteString("</li>\n")
		if i < len(lines) {
			if ordered && !reOL.MatchString(lines[i]) || !ordered && !reUL.MatchString(lines[i]) {
				break
			}
		}
	}
	out.WriteString("</" + tag + ">\n")
	return i
}

func writeItem(out *bytes.Buffer, item []string) {
	simple := true
	for _, l := range item[1:] {
		if isBlank(l) || reUL.MatchString(l) || reOL.MatchString(l) || reFence.MatchString(l) || strings.HasPrefix(strings.TrimLeft(l, " "), ">") {
			simple = false
			break
		}
	}
	if simple {
		out.WriteString(inlineLines(trimAll(item)))
		return
	}
	// The first paragraph stays inline; nested blocks render as blocks.
	j := 0
	var first []string
	for j < len(item) && !isBlank(item[j]) && (j == 0 || !(reUL.MatchString(item[j]) || reOL.MatchString(item[j]) || reFence.MatchString(item[j]))) {
		first = append(first, strings.TrimSpace(item[j]))
		j++
	}
	out.WriteString(inlineLines(first) + "\n")
	renderBlocks(out, item[j:])
}

func trimAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cur strings.Builder
	inCode := false
	for k := 0; k < len(line); k++ {
		c := line[k]
		switch {
		case c == '\\' && k+1 < len(line) && line[k+1] == '|':
			cur.WriteByte('|')
			k++
		case c == '`':
			inCode = !inCode
			cur.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func renderTable(out *bytes.Buffer, lines []string, i int) int {
	head := splitRow(lines[i])
	seps := splitRow(lines[i+1])
	align := make([]string, len(head))
	for k := range head {
		if k < len(seps) {
			s := seps[k]
			left, right := strings.HasPrefix(s, ":"), strings.HasSuffix(s, ":")
			switch {
			case left && right:
				align[k] = ` style="text-align:center"`
			case right:
				align[k] = ` style="text-align:right"`
			}
		}
	}
	out.WriteString(`<div class="tbl"><table><thead><tr>`)
	for k, h := range head {
		out.WriteString("<th" + align[k] + ">" + inline(h) + "</th>")
	}
	out.WriteString("</tr></thead><tbody>\n")
	i += 2
	for i < len(lines) && !isBlank(lines[i]) && strings.Contains(lines[i], "|") {
		row := splitRow(lines[i])
		out.WriteString("<tr>")
		for k := range head {
			cell := ""
			if k < len(row) {
				cell = row[k]
			}
			out.WriteString("<td" + align[k] + ">" + inline(cell) + "</td>")
		}
		out.WriteString("</tr>\n")
		i++
	}
	out.WriteString("</tbody></table></div>\n")
	return i
}

var (
	reCode     = regexp.MustCompile("(`+)(.+?)(`+)")
	reImage    = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?((?:[^()\s>]|\([^()\s]*\))*)>?(?:\s+"([^"]*)")?\s*\)`)
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(\s*<?((?:[^()\s>]|\([^()\s]*\))*)>?(?:\s+"([^"]*)")?\s*\)`)
	reAutolink = regexp.MustCompile(`&lt;(https?://[^\s<>]+?)&gt;`)
	reBareURL  = regexp.MustCompile(`(^|[\s(])(https?://[^\s<>()]+[^\s<>().,;:!?'"])`)
	reStrong   = regexp.MustCompile(`(\*\*|__)([^\s*_](?:.*?[^\s])?)(\*\*|__)`)
	reEm       = regexp.MustCompile(`(^|[^\w*])[*_]([^\s*_](?:[^*_]*?[^\s*_])?)[*_]($|[^\w*])`)
	reStrike   = regexp.MustCompile(`~~([^~]+)~~`)
)

// safeHref keeps http(s), mailto and relative targets; anything else
// (javascript:, data:, file:) becomes "#".
func safeHref(raw string) string {
	// Decode entities until stable (a browser decodes "java&#x73;cript:" or
	// "javascript&colon;" in an attribute) and drop whitespace and control
	// characters (browsers ignore them in a scheme: "java\tscript:").
	for i := 0; i < 4; i++ {
		d := html.UnescapeString(raw)
		if d == raw {
			break
		}
		raw = d
	}
	raw = strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	// A ':' before any '/', '?' or '#' is a scheme, parsed or not.
	if i := strings.IndexAny(raw, ":/?#"); i >= 0 && raw[i] == ':' {
		switch strings.ToLower(raw[:i]) {
		case "http", "https", "mailto":
		default:
			return "#"
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "#"
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return raw
	}
	return "#"
}

// inline renders one line of inline Markdown. The text is escaped first;
// code spans are cut out before any other rule so their content stays literal.
func inline(s string) string {
	var codes []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		parts := reCode.FindStringSubmatch(m)
		if len(parts[1]) != len(parts[3]) {
			return m
		}
		codes = append(codes, "<code>"+html.EscapeString(strings.TrimSpace(parts[2]))+"</code>")
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	s = html.EscapeString(s)
	var links []string
	hold := func(h string) string {
		links = append(links, h)
		return "\x01" + strconv.Itoa(len(links)-1) + "\x01"
	}
	s = reImage.ReplaceAllStringFunc(s, func(m string) string {
		p := reImage.FindStringSubmatch(m)
		src := safeHref(html.UnescapeString(p[2]))
		return hold(`<img src="` + html.EscapeString(src) + `" alt="` + p[1] + `" loading="lazy">`)
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		p := reLink.FindStringSubmatch(m)
		href := safeHref(html.UnescapeString(p[2]))
		return hold(`<a href="` + html.EscapeString(href) + `">` + emphasis(p[1]) + `</a>`)
	})
	s = reAutolink.ReplaceAllStringFunc(s, func(m string) string {
		u := html.UnescapeString(reAutolink.FindStringSubmatch(m)[1])
		return hold(`<a href="` + html.EscapeString(u) + `">` + html.EscapeString(u) + `</a>`)
	})
	s = reBareURL.ReplaceAllStringFunc(s, func(m string) string {
		p := reBareURL.FindStringSubmatch(m)
		u := html.UnescapeString(p[2])
		return p[1] + hold(`<a href="`+html.EscapeString(u)+`">`+html.EscapeString(u)+`</a>`)
	})
	s = emphasis(s)
	for k := len(links) - 1; k >= 0; k-- {
		s = strings.ReplaceAll(s, "\x01"+strconv.Itoa(k)+"\x01", links[k])
	}
	for k := len(codes) - 1; k >= 0; k-- {
		s = strings.ReplaceAll(s, "\x00"+strconv.Itoa(k)+"\x00", codes[k])
	}
	return s
}

func emphasis(s string) string {
	s = reStrong.ReplaceAllString(s, "<strong>$2</strong>")
	for range 2 {
		s = reEm.ReplaceAllString(s, "$1<em>$2</em>$3")
	}
	return reStrike.ReplaceAllString(s, "<del>$1</del>")
}
