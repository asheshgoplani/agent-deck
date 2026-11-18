package openpages

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderHTMLInlinesRelativeAssetsUnderRoots(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	writeFile(t, filepath.Join(root, "site/index.html"), `<!doctype html><html><head><title>Plan  v2</title>
<link rel="stylesheet" href="css/app.css"><script src="app.js"></script></head>
<body><img src="img/a.png" srcset="img/a.png 1x, img/missing.png 2x"><img src="../../secret.png">
<img src="https://example.com/x.png"><a href="other.html">next</a><div style="background:url('img/a.png')"></div></body></html>`)
	writeFile(t, filepath.Join(root, "site/css/app.css"), `body{background:url(../img/a.png)} @import "more.css";`)
	writeFile(t, filepath.Join(root, "site/css/more.css"), `h1{color:red}`)
	writeFile(t, filepath.Join(root, "site/app.js"), `console.log("hi")`)
	writeFile(t, filepath.Join(root, "site/img/a.png"), "\x89PNG\r\n\x1a\nDATA")
	writeFile(t, filepath.Join(tmp, "secret.png"), "\x89PNG SECRET")
	if err := os.Symlink(filepath.Join(tmp, "secret.png"), filepath.Join(root, "site/img/link.png")); err != nil {
		t.Fatal(err)
	}
	r, err := Render([]string{root}, Page{Path: filepath.Join(root, "site/index.html"), Kind: KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	if r.Title != "Plan v2" {
		t.Fatalf("title %q", r.Title)
	}
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nDATA"))
	for _, want := range []string{png, "<style>", `<script src="app.js"></script>`, // scripts are never inlined (phone policy)
		"data:text/css;base64,", `href="other.html"`, `src="https://example.com/x.png"`, `src="../../secret.png"`} {
		if !strings.Contains(doc, want) {
			t.Fatalf("missing %q in\n%s", want, doc)
		}
	}
	if strings.Contains(doc, base64.StdEncoding.EncodeToString([]byte("\x89PNG SECRET"))) {
		t.Fatal("a file outside the root was inlined")
	}
	if strings.Contains(doc, "data:text/javascript") {
		t.Fatal("a script was inlined")
	}
	if r.Skipped < 2 || r.Inlined < 4 {
		t.Fatalf("inlined %d skipped %d", r.Inlined, r.Skipped)
	}
	// A symlinked asset that leaves the root is skipped too.
	writeFile(t, filepath.Join(root, "site/l.html"), `<img src="img/link.png">`)
	r, err = Render([]string{root}, Page{Path: filepath.Join(root, "site/l.html"), Kind: KindHTML})
	if err != nil || strings.Contains(string(r.HTML), "data:image") || r.Skipped != 1 {
		t.Fatalf("symlink escape: %v %s", err, r.HTML)
	}
}

func TestRenderRefusesPagesOutsideRootsAndTooLarge(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	writeFile(t, filepath.Join(root, "ok.md"), "# hi")
	writeFile(t, filepath.Join(tmp, "outside.html"), "<p>secret</p>")
	for _, p := range []string{filepath.Join(tmp, "outside.html"), filepath.Join(root, "../outside.html"), "/etc/passwd"} {
		if _, err := Render([]string{root}, Page{Path: p, Kind: KindHTML}); !errors.Is(err, filelinks.ErrOutside) {
			t.Fatalf("%s: want outside, got %v", p, err)
		}
	}
	if _, err := Render(nil, Page{Path: filepath.Join(root, "ok.md"), Kind: KindMarkdown}); err == nil {
		t.Fatal("no roots must refuse")
	}
	writeFile(t, filepath.Join(root, "big.html"), strings.Repeat("a", int(MaxSourceBytes)+1))
	if _, err := Render([]string{root}, Page{Path: filepath.Join(root, "big.html"), Kind: KindHTML}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

func TestRenderMarkdownIsSafeAndPhoneSized(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "notes.md"), "# Brainstorm\n\nSome **bold** and *it* and `code <b>`.\n\n<script>alert(1)</script>\n\n"+
		"| a | b |\n|---|--:|\n| 1 | `x|y` |\n\n- [x] done\n- [ ] todo\n\n1. one\n2. two\n\n```go\nfmt.Println(\"<hi>\")\n```\n\n"+
		"> quote\n\n[bad](javascript:alert(1)) [good](https://example.com) ![pic](p.png)\n\n---\n")
	writeFile(t, filepath.Join(tmp, "p.png"), "\x89PNG\r\n\x1a\nX")
	r, err := Render([]string{tmp}, Page{Path: filepath.Join(tmp, "notes.md"), Kind: KindMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	if r.Title != "Brainstorm" {
		t.Fatalf("title %q", r.Title)
	}
	for _, want := range []string{`<meta name="viewport"`, "prefers-color-scheme:dark", "<h1>Brainstorm</h1>", "<strong>bold</strong>",
		"<em>it</em>", "<code>code &lt;b&gt;</code>", "&lt;script&gt;alert(1)&lt;/script&gt;", `<div class="tbl"><table>`,
		`<td style="text-align:right"><code>x|y</code></td>`, `checked disabled`, "<ol>", `class="language-go"`,
		"fmt.Println(&#34;&lt;hi&gt;&#34;)", "<blockquote>", `<a href="#">bad</a>`, `<a href="https://example.com">good</a>`,
		`src="data:image/png;base64,`, "<hr>"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("missing %q in\n%s", want, doc)
		}
	}
	// Table cells keep whole words: the table scrolls inside .tbl instead.
	if !strings.Contains(doc, "th,td{") || !strings.Contains(doc, "overflow-wrap:normal") {
		t.Fatalf("table cells must not break words:\n%s", doc)
	}
	if strings.Contains(doc, "<script>") || strings.Contains(doc, "javascript:") {
		t.Fatalf("unsafe markup survived:\n%s", doc)
	}
}
