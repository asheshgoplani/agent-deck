package openpages

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phone finish (18 Nov 2026), review m2 / feature sweep D1 residual gaps:
// what reaches the phone is decided by an ALLOWLIST of file types first
// (pages: .html .htm .md .markdown .txt; assets: the image, font and
// stylesheet types), with the private-name rules as a second layer. A .css
// asset is inlined only when it really is a stylesheet.

func TestPhonePagesAreAnAllowlistOfFileTypes(t *testing.T) {
	for _, name := range []string{"a.html", "A.HTM", "b.md", "c.markdown", "d.txt", "E.TXT"} {
		if PageKind(name) == "" {
			t.Errorf("%s: an allowed page type is not shown", name)
		}
	}
	for _, name := range []string{"a.xhtml", "b.mdown", "c.mkd", "d.text", "e.log", "f.csv", "g.png", "h.jpg", "i.svg",
		"j.json", "k.yaml", "l.toml", "m.ini", "n.conf", "o.sh", "p.js", "q.css", "r.sqlite", "s", "t.bak", "u.env", "v.html.bak"} {
		if k := PageKind(name); k != "" {
			t.Errorf("%s: not on the page allowlist, got kind %q", name, k)
		}
	}
}

func TestPhonePageAllowlistAppliesToOpenAndServe(t *testing.T) {
	f := newFixture(t)
	w := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.proj, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("server.log", "token=abc "+marker)
	w("export.csv", "user,pass\nroot,"+marker)
	w("notes.text", marker)
	w("page.xhtml", "<html><body>"+marker+"</body></html>")
	for _, rel := range []string{"server.log", "export.csv", "notes.text", "page.xhtml", "assets/pic.png"} {
		path := filepath.Join(f.proj, rel)
		if _, _, err := CheckPage([]string{f.proj}, path); !errors.Is(err, ErrUnsupported) {
			t.Errorf("CheckPage(%s) = %v, want ErrUnsupported", rel, err)
		}
		r, err := Render([]string{f.proj}, Page{Path: path, Kind: KindOf(path)})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Render(%s) = %v, want ErrUnsupported", rel, err)
		}
		noSecret(t, rel, r.HTML)
	}
	// the name rules still hold inside the allowlist (second layer)
	for _, rel := range []string{"db_password.txt", "my-secret.md", "secrets/notes.md", ".hidden/page.html"} {
		if _, _, err := CheckPage([]string{f.proj}, filepath.Join(f.proj, rel)); !errors.Is(err, ErrPrivate) {
			t.Errorf("CheckPage(%s) = %v, want ErrPrivate", rel, err)
		}
	}
}

// The sweep's fake.css (RENAMEDCSS-CANARY{}) and harmless.css (a dotenv line)
// and other text renamed to .css are not inlined; a real stylesheet is.
func TestRenamedTextIsNotInlinedAsCSS(t *testing.T) {
	f := newFixture(t)
	files := map[string]string{
		"fake.css":     "RENAMEDCSS-CANARY{}\n",
		"harmless.css": "SECRET_TOKEN=DOTENV-CANARY\n",
		"json.css":     `{"api_key": "JSON-CANARY"}`,
		"yaml.css":     "db:\n  password: YAML-CANARY\n",
		"pem.css":      "-----BEGIN OPENSSH PRIVATE KEY-----\nPEM-CANARY\n-----END OPENSSH PRIVATE KEY-----\n",
		"ini.css":      "[default]\naws_secret_access_key = INI-CANARY\n",
		"words.css":    "just some notes WORDS-CANARY\n",
		"real.css":     "/* site */\n@charset \"utf-8\";\n@import \"more.css\";\n:root{--accent:#06c}\nbody{font:16px/1.5 sans-serif;background:url(data:image/png;base64,iVBORw0KGgo=)}\n@media (prefers-color-scheme: dark){body{color:#eee}}\n@font-face{font-family:x;src:url(font.woff2) format(\"woff2\")}\n@keyframes spin{from{transform:rotate(0)}to{transform:rotate(360deg)}}\n.card{&:hover{color:red} padding:4px!important}\n",
		"more.css":     ".more{color:green}",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(f.proj, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	page := `<html><head><link rel="stylesheet" href="fake.css"><link rel="stylesheet" href="harmless.css"><link rel="stylesheet" href="json.css">
<link rel="stylesheet" href="yaml.css"><link rel="stylesheet" href="pem.css"><link rel="stylesheet" href="ini.css"><link rel="stylesheet" href="real.css">
<style>@import "words.css"; @import "fake.css"; .a{color:blue}</style></head><body>x</body></html>`
	if err := os.WriteFile(filepath.Join(f.proj, "css.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Render([]string{f.proj}, Page{Path: filepath.Join(f.proj, "css.html"), Kind: KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	for _, c := range []string{"RENAMEDCSS-CANARY", "DOTENV-CANARY", "JSON-CANARY", "YAML-CANARY", "PEM-CANARY", "INI-CANARY", "WORDS-CANARY"} {
		if strings.Contains(doc, c) || strings.Contains(doc, base64.StdEncoding.EncodeToString([]byte(c))) || containsB64Of(doc, files, c) {
			t.Errorf("%s: text that is not a stylesheet was inlined:\n%s", c, doc)
		}
	}
	if !strings.Contains(doc, "<style>/* site */") || !strings.Contains(doc, ".card{&:hover{color:red}") {
		t.Errorf("a real stylesheet is inlined:\n%s", doc)
	}
	if !strings.Contains(doc, "data:text/css;base64,"+base64.StdEncoding.EncodeToString([]byte(files["more.css"]))) {
		t.Errorf("a real @import is inlined:\n%s", doc)
	}
}

// containsB64Of: is the base64 of the whole file holding canary c in doc?
func containsB64Of(doc string, files map[string]string, c string) bool {
	for _, body := range files {
		if strings.Contains(body, c) && strings.Contains(doc, base64.StdEncoding.EncodeToString([]byte(body))) {
			return true
		}
	}
	return false
}

func TestIsStylesheet(t *testing.T) {
	yes := []string{
		"body{color:red}",
		"/* only a comment */ a{color:red}",
		"@import url(\"x.css\");",
		"@import 'x.css' screen;\nh1 { color : #06c ; }",
		"a{background:url(data:image/png;base64,AAA=);color:red}",
		"a[href$=\".pdf\"]::after{content:\"{;}\"}",
		"@media screen and (max-width:600px){.a{display:none}.b{display:block}}",
		"@supports (display:grid){@media print{.x{color:black}}}",
		"@font-face{font-family:\"X\";src:url(x.woff2) format(\"woff2\")}",
		"@keyframes k{0%{opacity:0}50.5%{opacity:.5}100%{opacity:1}}",
		":root{--gap: 4px; --empty:;}\n.x{gap:var(--gap)}",
		".a{color:red;;}",
		".parent{color:red; & .child{color:blue} .x &{color:green}}",
		"@layer base, components;\n@layer base{html{color:black}}",
		"\uFEFFbody{margin:0}",
	}
	no := []string{
		"", "   ", "/* nothing */",
		"RENAMEDCSS-CANARY{}",
		"a{}",
		"SECRET_TOKEN=abc",
		"KEY=value\nOTHER=x\n",
		`{"a":"b"}`,
		"db:\n  password: x\n",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
		"[default]\nkey = x",
		"hello world",
		"a{color:red",
		"a{color:red}}",
		"a{color red}",
		"a{\"color\":red}",
		"a{password=1}",
		"body{color:red} trailing words",
		"@media screen{RENAMED{}}",
		"a{color:\"unterminated}",
		"a{color:red}/* open comment",
	}
	for _, s := range yes {
		if !isStylesheet([]byte(s)) {
			t.Errorf("isStylesheet(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isStylesheet([]byte(s)) {
			t.Errorf("isStylesheet(%q) = true, want false", s)
		}
	}
}

// No false negatives on real stylesheets: the repo's own CSS (web UI, xterm)
// all parse.
func TestIsStylesheetAcceptsRealStylesheets(t *testing.T) {
	paths, _ := filepath.Glob("../web/static/*.css")
	more, _ := filepath.Glob("../web/static/*/*.css")
	paths = append(paths, more...)
	if len(paths) < 3 {
		t.Fatalf("expected the web UI stylesheets, found %v", paths)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !isStylesheet(data) {
			t.Errorf("%s: a real stylesheet is refused", p)
		}
	}
}
