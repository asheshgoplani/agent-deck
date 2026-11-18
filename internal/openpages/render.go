package openpages

import (
	"bytes"
	"crypto/sha256"
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
	head := `<meta http-equiv="Content-Security-Policy" content="` + DocumentCSP(doc) + `">` +
		`<meta http-equiv="x-dns-prefetch-control" content="off">`
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
	return "<!doctype html>\n<html><head><meta http-equiv=\"Content-Security-Policy\" content=\"" + DocumentCSP(nil) + "\"><meta http-equiv=\"x-dns-prefetch-control\" content=\"off\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
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
	raw := "" // the raw-text element the tokenizer is in, if any
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			return out.Bytes()
		}
		rawBytes := z.Raw()
		switch kind {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			t := z.Token()
			raw = ""
			if kind == xhtml.StartTagToken && rawTextTags[t.Data] {
				raw = t.Data
			}
			if rewritten, ok := in.rewriteTag(from, t); ok {
				out.WriteString(rewritten)
				continue
			}
		case xhtml.EndTagToken:
			raw = ""
		case xhtml.TextToken:
			if raw != "" {
				text := string(rawBytes)
				if raw == "style" {
					text = in.inlineCSS(from, text, 0)
				}
				out.WriteString(escapeLoaders(raw, text))
				continue
			}
		}
		out.Write(rawBytes)
	}
}

// rawTextTags are the elements whose content the tokenizer reads as raw
// text, where a browser may still read markup (scripting off for noscript,
// foreign content for svg/math style and script).
var rawTextTags = map[string]bool{"script": true, "style": true, "noscript": true, "iframe": true, "noembed": true,
	"noframes": true, "xmp": true, "textarea": true, "title": true, "plaintext": true}

var reLoader = regexp.MustCompile(`(?i)<(\s*(?:link|iframe|frame|object|embed)(?:[\s/>]|$))`)

// escapeLoaders makes every tag that loads inside raw text plain text however
// a parser reads it: \x3c in a script (a JS string keeps its value), \3c in a
// stylesheet, &lt; elsewhere (shown the same where entities are decoded).
func escapeLoaders(raw, text string) string {
	esc := "&lt;"
	switch raw {
	case "script":
		esc = `\x3c`
	case "style":
		esc = `\3c `
	}
	return reLoader.ReplaceAllString(text, strings.ReplaceAll(esc, "$", "$$")+"$1")
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
		// Allowlist (review rework 1, M2): a <link> survives only as an
		// inlined stylesheet (a <style>) or an inlined icon (data:). Every
		// other one is dropped: preconnect, dns-prefetch, prefetch, preload,
		// modulepreload, prerender, next, manifest … make the engine connect
		// with no script and no CSP check.
		rel, _ := attr(t, "rel")
		href, i := attr(t, "href")
		kind := linkKind(rel)
		if i >= 0 && kind == "stylesheet" {
			if real, uri, ok := in.asset(from, href); ok && strings.HasPrefix(uri, "data:text/css;") {
				raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:text/css;base64,"))
				return "<style>" + in.inlineCSS(real, string(raw), 0) + "</style>", true
			}
		}
		if i >= 0 && kind == "icon" {
			if _, uri, ok := in.asset(from, href); ok && strings.HasPrefix(uri, "data:image/") {
				t.Attr[i].Val = uri
				return t.String(), true
			}
		}
		return "", true
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
	case "iframe", "frame", "object", "embed":
		// A javascript: source runs in the frame's own realm, where no
		// guard may reach (a frame in a closed shadow root): never kept.
		for i, a := range t.Attr {
			if (a.Key == "src" || a.Key == "data") && isJavascriptURL(a.Val) {
				t.Attr[i].Val = "about:blank"
				changed = true
			}
			// a srcdoc document is parsed like a page (its links would
			// connect even with scripts off): the same rewrite, nested too
			if a.Key == "srcdoc" && (t.Data == "iframe" || t.Data == "frame") {
				if v := string(in.inlineHTML(from, []byte(a.Val))); v != a.Val {
					t.Attr[i].Val = v
					changed = true
				}
			}
		}
		if changed {
			return t.String(), true
		}
		return "", false
	case "script", "audio", "source", "track":
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

// linkKind is "stylesheet" or "icon" when every rel token is one of those
// kinds, else "" (the link is dropped).
func linkKind(rel string) string {
	kind := ""
	for _, tok := range strings.Fields(strings.ToLower(rel)) {
		k := ""
		switch tok {
		case "stylesheet":
			k = "stylesheet"
		case "icon", "shortcut", "apple-touch-icon", "apple-touch-icon-precomposed", "mask-icon":
			k = "icon"
		default:
			return ""
		}
		if kind != "" && kind != k {
			return ""
		}
		kind = k
	}
	return kind
}

// isJavascriptURL reports whether v is a javascript: URL the way a browser
// reads it (leading/trailing C0 controls and spaces stripped, tabs and
// newlines removed anywhere, scheme case-insensitive).
func isJavascriptURL(v string) bool {
	v = strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
	v = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v)
	return len(v) >= 11 && strings.EqualFold(v[:11], "javascript:")
}

// DocumentCSP is PageCSP for one document: script-src allows exactly the
// inline scripts the document holds, by SHA-256 hash ('none' when it has
// none), never 'unsafe-inline' (review rework 1, M1). A javascript: URL, an
// inline event handler or a script injected later is refused by the engine,
// in every frame that inherits the policy.
func DocumentCSP(doc []byte) string {
	var hashes []string
	seen := map[string]bool{}
	z := xhtml.NewTokenizer(bytes.NewReader(doc))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		if tt != xhtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "script" {
			continue
		}
		if _, i := attr(t, "src"); i >= 0 {
			continue
		}
		if typ, _ := attr(t, "type"); strings.EqualFold(strings.TrimSpace(typ), "speculationrules") {
			continue // not code, but it could make the engine prefetch: never allowed
		}
		if z.Next() != xhtml.TextToken {
			continue
		}
		text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(z.Raw()))
		sum := sha256.Sum256([]byte(text))
		h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	src := "'none'"
	if len(hashes) > 0 {
		src = strings.Join(hashes, " ")
	}
	return strings.Replace(PageCSP, "script-src 'unsafe-inline'", "script-src "+src, 1)
}
