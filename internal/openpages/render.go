package openpages

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
)

// Limits of a rendered page.
const (
	MaxSourceBytes   int64 = 4 << 20  // the page file itself
	MaxAssetBytes    int64 = 8 << 20  // one inlined asset
	MaxDocumentBytes int64 = 12 << 20 // the whole self-contained document
)

// ErrTooLarge is returned when the page file itself is over MaxSourceBytes.
var ErrTooLarge = errors.New("page is larger than the page limit")

// Rendered is one page as a single self-contained HTML document.
type Rendered struct {
	HTML       []byte
	Title      string
	Inlined    int
	Skipped    int
	ModifiedAt time.Time
}

// Render reads p from disk under roots and returns it as one document a
// phone WebView can show from a string: HTML as written with its relative
// assets inlined as data: URIs, Markdown rendered by RenderMarkdown, text in
// <pre>, an image in <img>. Every file, the page and each asset, is opened
// through filelinks.Open, so the roots, symlink and size rules apply to all.
func Render(roots []string, p Page) (Rendered, error) {
	if p.Kind == KindURL || p.Path == "" {
		return Rendered{Title: p.Title}, nil
	}
	f, err := filelinks.Open(roots, p.Path)
	if err != nil {
		return Rendered{}, err
	}
	defer f.Close()
	if f.Size > MaxSourceBytes {
		return Rendered{}, fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, f.Size, MaxSourceBytes)
	}
	info, _ := f.Stat()
	src, err := io.ReadAll(f.Reader())
	if err != nil {
		return Rendered{}, err
	}
	in := &inliner{roots: roots, budget: MaxDocumentBytes - int64(len(src))}
	r := Rendered{Title: p.Title}
	if info != nil {
		r.ModifiedAt = info.ModTime().UTC()
	}
	kind := p.Kind
	if kind == "" {
		kind = KindOf(p.Path)
	}
	switch kind {
	case KindHTML:
		r.HTML = in.inlineHTML(f.Path, src)
		if t := htmlTitle(src); t != "" {
			r.Title = t
		}
	case KindMarkdown:
		if t := markdownTitle(src); t != "" {
			r.Title = t
		}
		r.HTML = in.inlineHTML(f.Path, RenderMarkdown(src, r.Title))
	case KindImage:
		uri, ok := dataURI(f.Path, src)
		if !ok {
			uri = ""
		}
		r.HTML = []byte(wrapBody(r.Title, `<img alt="`+html.EscapeString(p.Name)+`" src="`+uri+`" style="max-width:100%;height:auto">`))
	default:
		r.HTML = []byte(wrapBody(r.Title, `<pre style="white-space:pre-wrap;font:14px/1.5 ui-monospace,Menlo,monospace;padding:12px">`+html.EscapeString(string(src))+`</pre>`))
	}
	r.Inlined, r.Skipped = in.inlined, in.skipped
	return r, nil
}

// TitleOf reads the page's own title (<title>, or the first "# " heading of
// Markdown) under roots; "" when it has none or cannot be read.
func TitleOf(roots []string, path, kind string) string {
	f, err := filelinks.Open(roots, path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if f.Size > MaxSourceBytes {
		return ""
	}
	src, err := io.ReadAll(f.Reader())
	if err != nil {
		return ""
	}
	switch kind {
	case KindHTML:
		return htmlTitle(src)
	case KindMarkdown:
		return markdownTitle(src)
	}
	return ""
}

func wrapBody(title, body string) string {
	return "<!doctype html>\n<html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
		"<meta name=\"color-scheme\" content=\"light dark\"><title>" + html.EscapeString(title) + "</title></head><body style=\"margin:0\">" + body + "</body></html>\n"
}

var reMDTitle = regexp.MustCompile(`(?m)^ {0,3}#[ \t]+(.+?)[ \t#]*$`)

func markdownTitle(src []byte) string {
	if m := reMDTitle.FindSubmatch(src); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

func htmlTitle(src []byte) string {
	z := xhtml.NewTokenizer(bytes.NewReader(src))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return ""
		case xhtml.StartTagToken:
			name, _ := z.TagName()
			if string(name) == "title" {
				if z.Next() == xhtml.TextToken {
					return strings.Join(strings.Fields(string(z.Text())), " ")
				}
				return ""
			}
			if string(name) == "body" {
				return ""
			}
		}
	}
}

type inliner struct {
	roots   []string
	budget  int64
	inlined int
	skipped int
}

// asset opens rel (relative to the folder of from) under the roots and
// returns its bytes, or false when it is outside, missing, too big or over
// the document budget.
func (in *inliner) asset(from, raw string) (string, []byte, bool) {
	target, ok := localRef(from, raw)
	if !ok {
		return "", nil, false
	}
	f, err := filelinks.Open(in.roots, target)
	if err != nil {
		in.skipped++
		return "", nil, false
	}
	defer f.Close()
	encoded := (f.Size + 2) / 3 * 4
	if f.Size > MaxAssetBytes || encoded > in.budget {
		in.skipped++
		return "", nil, false
	}
	data, err := io.ReadAll(f.Reader())
	if err != nil {
		in.skipped++
		return "", nil, false
	}
	in.budget -= encoded
	in.inlined++
	return f.Path, data, true
}

// localRef resolves a reference written in the page at from. Only relative
// references to files are local: a scheme, a host, a root path, a bare
// fragment or a data: URI is left alone.
func localRef(from, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	return filepath.Join(filepath.Dir(from), filepath.FromSlash(path.Clean(u.Path))), true
}

func mimeOf(name string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".css":
		return "text/css"
	case ".js", ".mjs":
		return "text/javascript"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func dataURI(name string, data []byte) (string, bool) {
	return "data:" + mimeOf(name, data) + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

// inlineCSS replaces url() and @import references of a stylesheet at from.
func (in *inliner) inlineCSS(from, css string, depth int) string {
	css = cssImport.ReplaceAllStringFunc(css, func(m string) string {
		g := cssImport.FindStringSubmatch(m)
		ref := g[1] + g[2]
		if depth > 3 {
			return m
		}
		real, data, ok := in.asset(from, ref)
		if !ok {
			return m
		}
		inner := in.inlineCSS(real, string(data), depth+1)
		return `@import url("data:text/css;base64,` + base64.StdEncoding.EncodeToString([]byte(inner)) + `")`
	})
	return cssURL.ReplaceAllStringFunc(css, func(m string) string {
		g := cssURL.FindStringSubmatch(m)
		ref := g[1] + g[2] + g[3]
		real, data, ok := in.asset(from, ref)
		if !ok {
			return m
		}
		uri, _ := dataURI(real, data)
		return `url("` + uri + `")`
	})
}

var (
	cssURL    = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^'")\s]*))\s*\)`)
	cssImport = regexp.MustCompile(`@import\s+(?:"([^"]*)"|'([^']*)')`)
)

// inlineHTML rewrites the page's relative loads to data: URIs and leaves
// everything else byte for byte. A stylesheet <link> becomes a <style>, a
// <script src> keeps its tag with a data: src.
func (in *inliner) inlineHTML(from string, src []byte) []byte {
	var out bytes.Buffer
	z := xhtml.NewTokenizer(bytes.NewReader(src))
	inStyle := false
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			return out.Bytes()
		}
		raw := z.Raw()
		switch kind {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			t := z.Token()
			inStyle = t.Data == "style" && kind == xhtml.StartTagToken
			if rewritten, ok := in.rewriteTag(from, t); ok {
				out.WriteString(rewritten)
				continue
			}
		case xhtml.EndTagToken:
			inStyle = false
		case xhtml.TextToken:
			if inStyle {
				out.WriteString(in.inlineCSS(from, string(raw), 0))
				continue
			}
		}
		out.Write(raw)
	}
}

func attr(t xhtml.Token, key string) (string, int) {
	for i, a := range t.Attr {
		if a.Key == key {
			return a.Val, i
		}
	}
	return "", -1
}

func (in *inliner) rewriteTag(from string, t xhtml.Token) (string, bool) {
	changed := false
	if t.Data == "link" {
		rel, _ := attr(t, "rel")
		href, _ := attr(t, "href")
		if strings.Contains(strings.ToLower(rel), "stylesheet") {
			if real, data, ok := in.asset(from, href); ok {
				return "<style>" + in.inlineCSS(real, string(data), 0) + "</style>", true
			}
			return "", false
		}
		if strings.Contains(strings.ToLower(rel), "icon") {
			if real, data, ok := in.asset(from, href); ok {
				_, i := attr(t, "href")
				t.Attr[i].Val, _ = dataURI(real, data)
				return t.String(), true
			}
		}
		return "", false
	}
	for i, a := range t.Attr {
		switch a.Key {
		case "src", "poster":
			if t.Data == "iframe" || t.Data == "frame" {
				continue
			}
			if real, data, ok := in.asset(from, a.Val); ok {
				t.Attr[i].Val, _ = dataURI(real, data)
				changed = true
			}
		case "data":
			if t.Data != "object" {
				continue
			}
			if real, data, ok := in.asset(from, a.Val); ok {
				t.Attr[i].Val, _ = dataURI(real, data)
				changed = true
			}
		case "srcset":
			parts := strings.Split(a.Val, ",")
			for k, candidate := range parts {
				fields := strings.Fields(candidate)
				if len(fields) == 0 {
					continue
				}
				if real, data, ok := in.asset(from, fields[0]); ok {
					fields[0], _ = dataURI(real, data)
					parts[k] = strings.Join(fields, " ")
					changed = true
				}
			}
			t.Attr[i].Val = strings.Join(parts, ", ")
		case "style":
			if css := in.inlineCSS(from, a.Val, 0); css != a.Val {
				t.Attr[i].Val = css
				changed = true
			}
		}
	}
	if !changed {
		return "", false
	}
	return t.String(), true
}
