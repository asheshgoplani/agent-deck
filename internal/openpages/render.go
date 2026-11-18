package openpages

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
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

// CheckPage applies the whole phone policy to a page file without reading
// it: under the roots (filelinks), not private, a kind the phone shows, not
// over MaxSourceBytes. It returns the resolved path and the kind.
func CheckPage(roots []string, path string) (string, string, error) {
	f, err := filelinks.Open(roots, path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	return checkOpened(roots, f)
}

func checkOpened(roots []string, f *filelinks.File) (string, string, error) {
	if Private(roots, f.Path) {
		return "", "", ErrPrivate
	}
	kind := PageKind(f.Path)
	if kind == "" || kind == KindText && !strings.HasPrefix(f.ContentType, "text/") {
		return "", "", ErrUnsupported
	}
	if f.Size > MaxSourceBytes {
		return "", "", fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, f.Size, MaxSourceBytes)
	}
	return f.Path, kind, nil
}

// Render reads p from disk under roots and returns it as one document a
// phone WebView can show from a string: HTML as written with its allowed
// relative assets inlined as data: URIs, Markdown rendered by
// RenderMarkdown, text in <pre>, an image in <img>. The page and every asset
// pass the phone policy (privacy.go) at every call, and the document carries
// PageCSP in a <meta> at its top.
func Render(roots []string, p Page) (Rendered, error) { return RenderLinked(roots, p, nil) }

// RenderLinked is Render with the session's page list: a relative link to a
// file that is on the list becomes "/@page/<page id>", so a viewer opens
// exactly that page (two index.html in different folders stay apart).
func RenderLinked(roots []string, p Page, links map[string]string) (Rendered, error) {
	if p.Kind == KindURL || p.Path == "" {
		return Rendered{Title: p.Title}, nil
	}
	f, err := filelinks.Open(roots, p.Path)
	if err != nil {
		return Rendered{}, err
	}
	defer f.Close()
	real, kind, err := checkOpened(roots, f)
	if err != nil {
		return Rendered{}, err
	}
	info, _ := f.Stat()
	src, err := io.ReadAll(f.Reader())
	if err != nil {
		return Rendered{}, err
	}
	in := &inliner{roots: roots, budget: MaxDocumentBytes - int64(len(src)), links: links}
	r := Rendered{Title: p.Title}
	if info != nil {
		r.ModifiedAt = info.ModTime().UTC()
	}
	switch kind {
	case KindHTML:
		r.HTML = withHead(in.inlineHTML(real, src))
		if t := htmlTitle(src); t != "" {
			r.Title = t
		}
	case KindMarkdown:
		if t := markdownTitle(src); t != "" {
			r.Title = t
		}
		r.HTML = withHead(in.inlineHTML(real, RenderMarkdown(src, r.Title)))
	case KindImage:
		t := assetType(real)
		if t == "" || !contentMatches(t, src) {
			return Rendered{}, ErrUnsupported
		}
		r.HTML = []byte(wrapBody(r.Title, `<img alt="`+html.EscapeString(p.Name)+`" src="data:`+t+`;base64,`+base64.StdEncoding.EncodeToString(src)+`" style="max-width:100%;height:auto">`))
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

var reViewport = regexp.MustCompile(`(?i)<meta[^>]+name\s*=\s*["']?viewport`)
var reDoctype = regexp.MustCompile(`(?is)^\s*(<\?xml[^>]*>\s*)?(<!doctype[^>]*>)?`)

// withHead puts the page policy, and a phone viewport when the page has
// none, at the very top of the document, before anything the page wrote:
// a later <meta http-equiv> can only add restrictions, never lift these.
func withHead(doc []byte) []byte {
	head := `<meta http-equiv="Content-Security-Policy" content="` + PageCSP + `">`
	if !reViewport.Match(doc) {
		head += `<meta name="viewport" content="width=device-width, initial-scale=1">`
	}
	loc := reDoctype.FindIndex(doc)
	at := 0
	if loc != nil {
		at = loc[1]
	}
	out := make([]byte, 0, len(doc)+len(head)+1)
	out = append(out, doc[:at]...)
	out = append(out, head...)
	return append(out, doc[at:]...)
}

func wrapBody(title, body string) string {
	return "<!doctype html>\n<html><head><meta http-equiv=\"Content-Security-Policy\" content=\"" + PageCSP + "\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
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
	links   map[string]string // resolved path -> page id
	roots   []string
	budget  int64
	inlined int
	skipped int
}

// asset returns raw (relative to the folder of from) as a data: URI when it
// passes the phone policy: under the roots, not private, an allowed asset
// type whose bytes match the type, within the size and document budgets.
func (in *inliner) asset(from, raw string) (string, string, bool) {
	target, ok := localRef(from, raw)
	if !ok {
		return "", "", false
	}
	skip := func() (string, string, bool) { in.skipped++; return "", "", false }
	if lexicallyPrivate(from, target) {
		return skip()
	}
	f, err := filelinks.Open(in.roots, target)
	if err != nil {
		return skip()
	}
	defer f.Close()
	t := assetType(f.Path)
	if t == "" || Private(in.roots, f.Path) {
		return skip()
	}
	encoded := (f.Size + 2) / 3 * 4
	if f.Size > MaxAssetBytes || encoded > in.budget {
		return skip()
	}
	data, err := io.ReadAll(f.Reader())
	if err != nil || !contentMatches(t, data) {
		return skip()
	}
	in.budget -= encoded
	in.inlined++
	return f.Path, "data:" + t + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

// lexicallyPrivate reports whether the written reference names a private
// component on its way (".env", "x/.git/y"), before any symlink resolution.
func lexicallyPrivate(from, target string) bool {
	rel, err := filepath.Rel(filepath.Dir(from), target)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part != ".." && PrivateName(part) {
			return true
		}
	}
	return false
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

// privateLink reports whether a relative link of the page points at a
// private name, lexically or after symlink resolution.
func (in *inliner) privateLink(from, raw string) bool {
	target, ok := localRef(from, raw)
	if !ok {
		return false
	}
	if lexicallyPrivate(from, target) {
		return true
	}
	if real, err := filepath.EvalSymlinks(target); err == nil {
		return Private(in.roots, real)
	}
	return false
}

// linkedPage is the page id of a relative link's target when that file is on
// the session's page list.
func (in *inliner) linkedPage(from, raw string) (string, bool) {
	if len(in.links) == 0 {
		return "", false
	}
	target, ok := localRef(from, raw)
	if !ok {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real
	}
	id, ok := in.links[target]
	return id, ok
}

// inlineCSS replaces url() and @import references of a stylesheet at from.
func (in *inliner) inlineCSS(from, css string, depth int) string {
	css = cssImport.ReplaceAllStringFunc(css, func(m string) string {
		g := cssImport.FindStringSubmatch(m)
		ref := g[1] + g[2]
		if depth > 3 {
			return `@import url("about:invalid")`
		}
		real, uri, ok := in.asset(from, ref)
		if !ok || !strings.HasPrefix(uri, "data:text/css;") {
			return `@import url("about:invalid")`
		}
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:text/css;base64,"))
		inner := in.inlineCSS(real, string(raw), depth+1)
		return `@import url("data:text/css;base64,` + base64.StdEncoding.EncodeToString([]byte(inner)) + `")`
	})
	return cssURL.ReplaceAllStringFunc(css, func(m string) string {
		g := cssURL.FindStringSubmatch(m)
		ref := g[1] + g[2] + g[3]
		if strings.HasPrefix(strings.TrimSpace(ref), "data:") || strings.HasPrefix(ref, "#") {
			return m
		}
		_, uri, ok := in.asset(from, ref)
		if !ok {
			// Not inlinable: drop the reference so nothing is fetched.
			return `url("about:invalid")`
		}
		return `url("` + uri + `")`
	})
}

var (
	cssURL    = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^'")\s]*))\s*\)`)
	cssImport = regexp.MustCompile(`@import\s+(?:"([^"]*)"|'([^']*)')`)
)

// inlineHTML rewrites the page's relative loads to data: URIs and leaves
// everything else byte for byte. A stylesheet <link> becomes a <style>.
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

// rewriteTag inlines what a tag loads. Only images (src, srcset, poster,
// icons), stylesheets and CSS url() are inlined; a script, frame, object or
// media src is never filled in (the page CSP blocks loading them anyway).
// A relative link to a private name becomes "#".
func (in *inliner) rewriteTag(from string, t xhtml.Token) (string, bool) {
	changed := false
	switch t.Data {
	case "link":
		rel, _ := attr(t, "rel")
		href, i := attr(t, "href")
		lrel := strings.ToLower(rel)
		if i >= 0 && strings.Contains(lrel, "stylesheet") {
			if real, uri, ok := in.asset(from, href); ok && strings.HasPrefix(uri, "data:text/css;") {
				raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:text/css;base64,"))
				return "<style>" + in.inlineCSS(real, string(raw), 0) + "</style>", true
			}
			return "", false
		}
		if i >= 0 && strings.Contains(lrel, "icon") {
			if _, uri, ok := in.asset(from, href); ok && strings.HasPrefix(uri, "data:image/") {
				t.Attr[i].Val = uri
				return t.String(), true
			}
		}
		return "", false
	case "a", "area":
		href, i := attr(t, "href")
		if i < 0 {
			return "", false
		}
		if in.privateLink(from, href) {
			t.Attr[i].Val = "#"
			return t.String(), true
		}
		if id, ok := in.linkedPage(from, href); ok {
			t.Attr[i].Val = "/@page/" + id
			return t.String(), true
		}
		return "", false
	case "script", "iframe", "frame", "object", "embed", "audio", "source", "track":
		return "", false
	}
	for i, a := range t.Attr {
		switch a.Key {
		case "src", "poster":
			if _, uri, ok := in.asset(from, a.Val); ok && strings.HasPrefix(uri, "data:image/") {
				t.Attr[i].Val = uri
				changed = true
			}
		case "srcset":
			parts := strings.Split(a.Val, ",")
			for k, candidate := range parts {
				fields := strings.Fields(candidate)
				if len(fields) == 0 {
					continue
				}
				if _, uri, ok := in.asset(from, fields[0]); ok && strings.HasPrefix(uri, "data:image/") {
					fields[0] = uri
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
