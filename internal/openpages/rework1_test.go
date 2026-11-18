package openpages

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Phone finish, review rework 1 (18 Nov 2026).
// M1: a page's scripts are allowed by HASH only (no 'unsafe-inline'): a
// javascript: URL (an iframe in a closed shadow root) or an injected inline
// handler is refused by the engine in every frame; javascript: sources of
// iframe/frame/object/embed are stripped at render too.
// M2: a <link> survives only when it is an inlined (data:) stylesheet or
// icon; preconnect, dns-prefetch, prefetch, preload, modulepreload,
// prerender, next and any other rel that makes the engine fetch are dropped.

func renderPage(t *testing.T, page string) string {
	t.Helper()
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.proj, "p.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Render([]string{f.proj}, Page{Path: filepath.Join(f.proj, "p.html"), Kind: KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	return string(r.HTML)
}

func scriptSrc(t *testing.T, doc string) string {
	t.Helper()
	m := regexp.MustCompile(`^(?:<!doctype html>)?<meta http-equiv="Content-Security-Policy" content="([^"]*)">`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatalf("no leading CSP meta:\n%s", doc)
	}
	for _, d := range strings.Split(m[1], ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src ") {
			return d
		}
	}
	t.Fatalf("no script-src in %q", m[1])
	return ""
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'"
}

func TestPageScriptsAllowedByHashOnly(t *testing.T) {
	a := "var o=1;\nchart();"
	b := "document.title='x'"
	doc := renderPage(t, "<!doctype html><html><head><script>"+a+"</script></head><body><button onclick=\"go()\">x</button>"+
		"<div><template shadowrootmode=\"closed\"><iframe src=\"javascript:(function(){new RTCPeerConnection()})()\"></iframe></template></div>"+
		"<iframe src=\" JaVaScRiPt:alert(1)\"></iframe><frame src=\"javascript:1\"><object data=\"javascript:2\"></object><embed src=\"javascript:3\">"+
		"<script>"+b+"</script><script src=\"app.js\"></script></body></html>")
	ss := scriptSrc(t, doc)
	if strings.Contains(ss, "unsafe-inline") {
		t.Fatalf("script-src must not allow unsafe-inline: %s", ss)
	}
	if ss != "script-src "+hashOf(a)+" "+hashOf(b) {
		t.Fatalf("script-src %q, want the two inline scripts' hashes", ss)
	}
	if strings.Contains(strings.ToLower(doc), "javascript:") {
		t.Fatalf("javascript: sources must be stripped from frames, objects and embeds:\n%s", doc)
	}
	if !strings.Contains(doc, "<script>"+a+"</script>") || !strings.Contains(doc, "<script>"+b+"</script>") {
		t.Fatalf("inline scripts kept as written:\n%s", doc)
	}
}

func TestPageWithoutScriptsAllowsNone(t *testing.T) {
	doc := renderPage(t, "<html><body><p onclick=\"x()\">hi</p></body></html>")
	if ss := scriptSrc(t, doc); ss != "script-src 'none'" {
		t.Fatalf("a page with no inline script: %q", ss)
	}
	md := []byte(RenderMarkdown([]byte("# T\n\ntext\n"), "T"))
	if ss := scriptSrc(t, string(withHead(md))); ss != "script-src 'none'" {
		t.Fatalf("Markdown: %q", ss)
	}
}

func TestPageScriptHashNormalisesNewlines(t *testing.T) {
	doc := renderPage(t, "<html><body><script>a();\r\nb();\rc();</script></body></html>")
	if ss := scriptSrc(t, doc); ss != "script-src "+hashOf("a();\nb();\nc();") {
		t.Fatalf("CRLF / CR are LF to the browser before hashing: %q", ss)
	}
}

func TestFetchingLinksAreDropped(t *testing.T) {
	doc := renderPage(t, `<html><head>
<link rel="preconnect" href="http://198.51.100.7:8497/x"><link rel="dns-prefetch" href="//leak.example">
<link rel="prefetch" href="https://leak.example/p"><link rel="preload" as="image" href="https://leak.example/i.png">
<link rel="modulepreload" href="https://leak.example/m.js"><link rel="prerender" href="https://leak.example/r">
<link rel="next" href="https://leak.example/n"><link rel="PRECONNECT" href="https://leak.example/u">
<link rel="icon preconnect" href="https://leak.example/ip"><link rel="stylesheet" href="https://leak.example/s.css">
<link rel="alternate stylesheet" href="https://leak.example/alt.css"><link rel="manifest" href="https://leak.example/m.json">
<link href="https://leak.example/norel"><LINK REL=preconnect HREF=https://leak.example/upper>
<link rel="stylesheet" href="app.css"><link rel="icon" href="assets/pic.png">
</head><body><svg><link rel="preconnect" href="https://leak.example/svg"/></svg>x</body></html>`)
	if strings.Contains(doc, "leak.example") || strings.Contains(doc, "198.51.100.7") {
		t.Fatalf("a link that makes the engine fetch survived:\n%s", doc)
	}
	if !strings.Contains(doc, "<style>p{color:teal}") || !strings.Contains(doc, `<link rel="icon" href="data:image/png;base64,`) {
		t.Fatalf("the local stylesheet and icon are still inlined:\n%s", doc)
	}
	if !strings.Contains(doc, `<meta http-equiv="x-dns-prefetch-control" content="off">`) {
		t.Fatalf("DNS prefetch of anchors is turned off:\n%s", doc)
	}
}

// Rework 1 (fin-srcdoc): a srcdoc document is parsed like any page, so its
// links would connect with scripts off; its text goes through the same
// rewrite (nested too). A speculationrules script is never hashed.
func TestSrcdocIsRewrittenAndSpeculationRulesNotHashed(t *testing.T) {
	doc := renderPage(t, `<html><body><iframe srcdoc="&lt;link rel=preconnect href=http://leak.example/a&gt;&lt;p&gt;one&lt;/p&gt;&lt;iframe srcdoc=&quot;&amp;lt;link rel=prefetch href=http://leak.example/b&amp;gt;&quot;&gt;&lt;/iframe&gt;"></iframe>`+
		`<script type="speculationrules">{"prefetch":[{"source":"list","urls":["http://leak.example/c"]}]}</script></body></html>`)
	if strings.Contains(doc, "leak.example/a") || strings.Contains(doc, "leak.example/b") {
		t.Fatalf("a link inside srcdoc survived:\n%s", doc)
	}
	if !strings.Contains(doc, "one") {
		t.Fatalf("the srcdoc text is kept:\n%s", doc)
	}
	if ss := scriptSrc(t, doc); ss != "script-src 'none'" {
		t.Fatalf("speculation rules must not be allowed by hash: %q", ss)
	}
}

// Rework 2: text the Go tokenizer reads as raw text (script, style,
// noscript, iframe, noembed, noframes, xmp, textarea, title) may be read as
// markup by a browser (scripting off for noscript, foreign content for svg
// style/script): a tag that loads inside it is escaped, never left as "<link".
// Inside a script the escape is \x3c (a JS string keeps its value), the same as
// the phone viewer.
func TestRawTextCannotHideALoadingTag(t *testing.T) {
	doc := renderPage(t, `<html><body><script>var s="<link rel=stylesheet href=x>"+"<iframe src=y>";</script>`+
		`<noscript><link rel=preconnect href=http://leak.example/n></noscript>`+
		`<svg><style><img><link rel=preconnect href=http://leak.example/s></style></svg>`+
		`<svg><script><img><link rel=preconnect href=http://leak.example/f></script></svg>`+
		`<textarea><link rel=prefetch href=http://leak.example/t></textarea></body></html>`)
	if regexp.MustCompile(`(?i)<(link|iframe)[^>]*leak\.example`).MatchString(doc) {
		t.Fatalf("a loading tag survived in raw text:\n%s", doc)
	}
	a := `var s="\x3clink rel=stylesheet href=x>"+"\x3ciframe src=y>";`
	if !strings.Contains(doc, "<script>"+a+"</script>") {
		t.Fatalf("script text: \\x3c escape expected:\n%s", doc)
	}
	if ss := scriptSrc(t, doc); !strings.Contains(ss, hashOf(a)) {
		t.Fatalf("the hash is of the escaped script: %q", ss)
	}
}
