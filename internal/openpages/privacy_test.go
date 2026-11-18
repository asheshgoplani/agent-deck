package openpages

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
)

// marker is in every private file of the fixture; it must never appear in
// anything rendered for the phone, plain or base64.
const marker = "SECRET-VALUE-42"

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\xcf\xc0\x00\x00\x03\x01\x01\x00\xc9\xfe\x92\xef\x00\x00\x00\x00IEND\xaeB`\x82")

// sweepEvil is the feature sweep's evil.html (openphone-test, 18 Nov 2026).
const sweepEvil = `<html><head><title>Evil</title>
<link rel="stylesheet" href="../other/out.css">
<link rel="stylesheet" href="/etc/hostname">
<style>@import "../.ssh/id_x"; .a{background:url(../other/secret.txt)} .b{background:url(.env)}</style>
</head><body>
<img src="/etc/passwd"><img src="file:///etc/passwd"><img src="../.ssh/id_x"><img src="../../../../../../etc/passwd">
<img src=".env"><script src=".env"></script><img src="keylink"><img src="assets/../.env">
<img srcset="../other/secret.txt 1x, .env 2x"><object data=".env"></object><video poster=".env"></video>
<iframe src=".env"></iframe><iframe src="/etc/passwd"></iframe>
<div style="background:url('.env')">x</div>
<img src="bigasset.png"><img src="linkdir/secret.txt"><img src="%2e%2e/other/secret.txt"><img src="..%2fother/secret.txt">
</body></html>`

// sweepNotes is the sweep's notes.md, with the hostile lines kept.
const sweepNotes = "# Project Notes <script>alert('t')</script>\n\nIntro with **bold**, *em*, `code <b>` and ~~strike~~.\n\n" +
	"![pic](assets/pic.png)\n![dotenv](.env)\n![outside](../other/secret.txt)\n![abs](/etc/passwd)\n![jsimg](javascript:alert(1))\n![alt\" onerror=\"alert(1)](assets/pic.png)\n\n" +
	"[other doc](docs/second.md) | [env](.env) | [git](.git/config) | [external](https://example.com/a?b=1&c=2) | [js](javascript:alert(1)) | [JS2](JaVaScRiPt:alert(1)) | " +
	"[data](data:text/html,<script>alert(1)</script>) | [file](file:///etc/passwd) | [vb](vbscript:x) | <https://auto.example.com> | <javascript:alert(2)> https://bare.example.com/x\n\n" +
	"<script>document.title='MD-SCRIPT-RAN'</script>\n<img src=x onerror=\"document.title='MD-ONERROR-RAN'\">\n<iframe src=\"https://evil.example\"></iframe>\n\n> quote line\n"

type fixture struct{ tmp, proj string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	w := func(rel string, data []byte) {
		t.Helper()
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sec := []byte(marker + "\n")
	for _, rel := range []string{"proj/.env", "proj/.env.local", "proj/.git/config", "proj/.aws/credentials", "proj/.npmrc", "proj/.netrc",
		"proj/.claude/settings.json", "proj/sub/.ssh/id_x", "proj/server.pem", "proj/tls.key", "proj/id_rsa", "proj/id_ed25519.pub",
		"proj/aws_credentials.txt", "proj/my-secret.md", "proj/db_password.txt", "proj/github-token.txt", "proj/cert.p12", "proj/vault.kdbx",
		"proj/.hidden/page.html", "proj/secrets/notes.md", ".ssh/id_x", "other/secret.txt", "other/out.css"} {
		w(rel, sec)
	}
	w("proj/assets/pic.png", pngBytes)
	w("proj/logo.png", sec) // a secret renamed as an image
	w("proj/app.css", []byte("p{color:teal} .x{background:url(assets/pic.png)} .y{background:url(.env)}"))
	w("proj/fake.css", append([]byte{0}, sec...)) // binary disguised as a stylesheet
	w("proj/font.woff2", append([]byte("wOF2"), make([]byte, 16)...))
	w("proj/app.js", []byte("console.log('"+marker+"')")) // scripts are never inlined
	w("proj/blob.bin", []byte{0, 1, 2, 3, 0, 5})
	w("proj/plain.txt", []byte("hello plain\n"))
	w("proj/report.html", []byte(`<html><head><title>Report</title></head><body><p>ok</p><img src="assets/pic.png"></body></html>`))
	w("proj/evil.html", []byte(sweepEvil))
	w("proj/notes.md", []byte(sweepNotes))
	w("proj/docs/second.md", []byte("# Second\n"))
	w("proj/huge.html", bytes.Repeat([]byte("a"), int(MaxSourceBytes)+1))
	w("proj/bigasset.png", append(append([]byte{}, pngBytes...), make([]byte, MaxAssetBytes)...))
	for link, target := range map[string]string{
		"proj/keylink": "../.ssh/id_x", "proj/linkdir": "../other", "proj/envlink.png": ".env", "proj/innocent.md": ".env",
		"proj/sshlink.html": "../.ssh/id_x", "proj/piclink.png": "assets/pic.png", "proj/gitlink.html": ".git/config",
	} {
		if err := os.Symlink(target, filepath.Join(tmp, link)); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{tmp: tmp, proj: proj}
}

func noSecret(t *testing.T, what string, doc []byte) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString([]byte(marker + "\n"))
	if bytes.Contains(doc, []byte(marker)) || bytes.Contains(doc, []byte(b64)) || bytes.Contains(doc, []byte(base64.StdEncoding.EncodeToString([]byte(marker)))) {
		t.Fatalf("%s: a private file reached the phone document:\n%s", what, doc)
	}
}

// Every page the sweep opened, plus the deny-by-default names: what CheckPage
// (the open path) and Render (the serve path) say.
func TestPhonePolicyPages(t *testing.T) {
	f := newFixture(t)
	roots := []string{f.proj}
	cases := []struct {
		rel  string
		want error // nil = shown
		kind string
	}{
		{"report.html", nil, KindHTML},
		{"notes.md", nil, KindMarkdown},
		{"plain.txt", nil, KindText},
		{"assets/pic.png", ErrUnsupported, ""}, // images are assets, not pages (page allowlist)
		{"piclink.png", ErrUnsupported, ""},
		{"docs/second.md", nil, KindMarkdown},
		{"evil.html", nil, KindHTML},
		{".env", ErrPrivate, ""},
		{".env.local", ErrPrivate, ""},
		{".git/config", ErrPrivate, ""},
		{".aws/credentials", ErrPrivate, ""},
		{".npmrc", ErrPrivate, ""},
		{".netrc", ErrPrivate, ""},
		{".claude/settings.json", ErrPrivate, ""},
		{"sub/.ssh/id_x", ErrPrivate, ""},
		{".hidden/page.html", ErrPrivate, ""},
		{"secrets/notes.md", ErrPrivate, ""},
		{"server.pem", ErrPrivate, ""},
		{"tls.key", ErrPrivate, ""},
		{"id_rsa", ErrPrivate, ""},
		{"id_ed25519.pub", ErrPrivate, ""},
		{"aws_credentials.txt", ErrPrivate, ""},
		{"my-secret.md", ErrPrivate, ""},
		{"db_password.txt", ErrPrivate, ""},
		{"github-token.txt", ErrPrivate, ""},
		{"cert.p12", ErrPrivate, ""},
		{"vault.kdbx", ErrPrivate, ""},
		{"innocent.md", ErrPrivate, ""},  // symlink to .env: private after resolution
		{"envlink.png", ErrPrivate, ""},  // same, disguised as an image
		{"gitlink.html", ErrPrivate, ""}, // symlink into .git
		{"sshlink.html", filelinks.ErrOutside, ""},
		{"keylink", filelinks.ErrOutside, ""},
		{"linkdir/secret.txt", filelinks.ErrOutside, ""},
		{"../other/secret.txt", filelinks.ErrOutside, ""},
		{"../.ssh/id_x", filelinks.ErrOutside, ""},
		{"blob.bin", ErrUnsupported, ""},
		{"app.js", ErrUnsupported, ""},
		{"app.css", ErrUnsupported, ""},
		{"huge.html", ErrTooLarge, ""},
	}
	for _, tc := range cases {
		path := filepath.Join(f.proj, tc.rel)
		_, kind, err := CheckPage(roots, path)
		if !errors.Is(err, tc.want) || err == nil && kind != tc.kind {
			t.Errorf("CheckPage(%s) = %q, %v; want %q, %v", tc.rel, kind, err, tc.kind, tc.want)
		}
		r, err := Render(roots, Page{Path: path, Kind: KindOf(path)})
		if !errors.Is(err, tc.want) {
			t.Errorf("Render(%s) err = %v; want %v", tc.rel, err, tc.want)
		}
		noSecret(t, tc.rel, r.HTML)
	}
	if _, _, err := CheckPage(roots, "/etc/passwd"); !errors.Is(err, filelinks.ErrOutside) {
		t.Errorf("/etc/passwd: %v", err)
	}
}

// The sweep's evil.html: every load of a private, outside or disallowed file
// is skipped; the one allowed image is inlined; nothing can be fetched.
func TestPhonePolicyEvilPageAssets(t *testing.T) {
	f := newFixture(t)
	r, err := Render([]string{f.proj}, Page{Path: filepath.Join(f.proj, "evil.html"), Kind: KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	noSecret(t, "evil.html", r.HTML)
	if strings.Contains(doc, "data:application/octet-stream") || strings.Contains(doc, "data:text/javascript") {
		t.Fatalf("a disallowed type was inlined:\n%s", doc)
	}
	if r.Inlined != 0 {
		t.Fatalf("evil.html loads nothing allowed (bigasset is over the asset cap), inlined %d:\n%s", r.Inlined, doc)
	}
	if !strings.HasPrefix(doc, `<meta http-equiv="Content-Security-Policy" content="`+PageCSP+`">`) {
		t.Fatalf("the page policy must come first:\n%s", doc[:200])
	}
	if !strings.Contains(doc, `name="viewport"`) {
		t.Fatal("an HTML page without a viewport gets one (sweep D4)")
	}
	if strings.Count(doc, `url("about:invalid")`) < 3 || strings.Contains(doc, "url(.env)") || strings.Contains(doc, "url('.env')") {
		t.Fatalf("CSS references to private files must be dropped:\n%s", doc)
	}
}

func TestPhonePolicyAllowedAssetsAndLinks(t *testing.T) {
	f := newFixture(t)
	page := `<!doctype html><html><head><meta name="viewport" content="width=400"><link rel="stylesheet" href="app.css"><link rel="stylesheet" href="fake.css">
<style>@font-face{src:url(font.woff2)} .z{background:url(logo.png)}</style></head><body>
<img src="assets/pic.png"><img src="logo.png"><img src="envlink.png"><script src="app.js"></script>
<a href=".env">env</a><a href=".git/config">git</a><a href="innocent.md">sneaky</a><a href="docs/second.md">second</a><a href="https://example.com">web</a><a href="#top">top</a>
</body></html>`
	if err := os.WriteFile(filepath.Join(f.proj, "mixed.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Render([]string{f.proj}, Page{Path: filepath.Join(f.proj, "mixed.html"), Kind: KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	noSecret(t, "mixed.html", r.HTML)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	for _, want := range []string{
		png, // the real image
		"<style>p{color:teal} .x{background:url(\"" + png,                      // stylesheet inlined, its image too
		`url("data:font/woff2;base64,`,                                         // font
		`<a href="#">env</a>`, `<a href="#">git</a>`, `<a href="#">sneaky</a>`, // private links neutralised
		`<a href="docs/second.md">second</a>`, `<a href="https://example.com">web</a>`, `<a href="#top">top</a>`,
		`<script src="app.js"></script>`, // never inlined; CSP blocks it
		`<!doctype html><meta http-equiv="Content-Security-Policy"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(doc, `name="viewport"`) != 1 {
		t.Error("a page with its own viewport keeps only its own")
	}
	if strings.Contains(doc, "fake.css") == false || strings.Contains(doc, "data:text/css;base64,"+base64.StdEncoding.EncodeToString(append([]byte{0}, []byte(marker+"\n")...))) {
		t.Error("a binary file named .css is not inlined")
	}
	if r.Inlined != 4 { // pic.png, app.css, its pic.png, font.woff2
		t.Errorf("inlined %d, want 4\n%s", r.Inlined, doc)
	}
}

func TestPhonePolicyMarkdown(t *testing.T) {
	f := newFixture(t)
	r, err := Render([]string{f.proj}, Page{Path: filepath.Join(f.proj, "notes.md"), Kind: KindMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(r.HTML)
	noSecret(t, "notes.md", r.HTML)
	for _, want := range []string{
		"data:image/png;base64,", `<a href="#">env</a>`, `<a href="#">git</a>`, `<a href="docs/second.md">other doc</a>`,
		`<a href="https://auto.example.com">https://auto.example.com</a>`, // sweep D8
		`<a href="https://bare.example.com/x">`, "&lt;script&gt;document.title", `Content-Security-Policy`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"<script>", "<iframe", `href="javascript:`, `src="javascript:`, `href="vbscript:`, `href="data:text/html`, `href="file:`, "onerror=\"", "#\">js</a>)"} {
		if strings.Contains(strings.ToLower(doc), strings.ToLower(bad)) {
			t.Errorf("unsafe %q survived", bad)
		}
	}
}

func TestPrivateName(t *testing.T) {
	for name, want := range map[string]bool{
		".env": true, ".env.production": true, ".git": true, ".ssh": true, ".aws": true, ".npmrc": true, ".netrc": true, ".claude": true,
		"server.PEM": true, "a.key": true, "id_rsa": true, "id_rsa.pub": true, "id_ecdsa": true, "credentials": true, "gcp-credentials.json": true,
		"client_secret.json": true, "Secrets": true, "password.txt": true, "token": true, "cert.pfx": true, "db.kdbx": true, "terraform.tfstate": true,
		"report.html": false, "notes.md": false, "pic.png": false, "index.html": false, "README.md": false, "keyboard.md": false, "..": false, ".": false,
	} {
		if got := PrivateName(name); got != want {
			t.Errorf("PrivateName(%q) = %v, want %v", name, got, want)
		}
	}
}
