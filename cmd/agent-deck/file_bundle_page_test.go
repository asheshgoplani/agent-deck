package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBundleTree creates files (name -> size or content) under a temp folder.
func writeBundleTree(t *testing.T, files map[string]string, sized map[string]int) string {
	t.Helper()
	folder := t.TempDir()
	write := func(name string, data []byte) {
		path := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		write(name, []byte(data))
	}
	for name, size := range sized {
		write(name, bytes.Repeat([]byte("x"), size))
	}
	return folder
}

func bundleNames(t *testing.T, archive []byte) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeDir {
			names[header.Name] = true
		}
	}
}

// The owner's prototype folder: index.html uses a few small files, while a
// screenshots folder only gallery.html uses is bigger than the whole cap.
// Opening index.html must bundle what it uses instead of failing on shots/.
func TestFileBundlePageSkipsUnusedLargeFiles(t *testing.T) {
	const limit = 64 << 10
	folder := writeBundleTree(t, map[string]string{
		"index.html": `<!doctype html><link rel="icon" href="assets/mark.svg"><link rel="stylesheet" href="tokens.css">` +
			`<style>body{background:url('assets/bg.svg')}</style><script src="js/main.js"></script>` +
			`<img srcset="assets/a.svg 1x, assets/b.svg 2x"><a href="https://example.com/x.html">x</a><a href="#top">top</a>`,
		"tokens.css":      `@import "base.css"; @font-face{src:url("assets/fonts/inter.woff2")}`,
		"base.css":        `body{color:red}`,
		"js/main.js":      `console.log("hi")`,
		"assets/mark.svg": "<svg/>", "assets/bg.svg": "<svg/>", "assets/a.svg": "<svg/>", "assets/b.svg": "<svg/>",
		"assets/fonts/inter.woff2": "font",
		"gallery.html":             `<img src="shots/one.png"><img src="shots/two.png">`,
	}, map[string]int{"shots/one.png": 40 << 10, "shots/two.png": 40 << 10})
	var out bytes.Buffer
	if err := writeFileBundle(&out, filepath.Join(folder, "index.html"), limit); err != nil {
		t.Fatalf("page bundle failed: %v", err)
	}
	if int64(out.Len()) > limit {
		t.Fatalf("bundle is %d bytes, over the %d cap", out.Len(), limit)
	}
	names := bundleNames(t, out.Bytes())
	for _, want := range []string{"index.html", "tokens.css", "base.css", "js/main.js", "assets/mark.svg", "assets/bg.svg", "assets/a.svg", "assets/b.svg", "assets/fonts/inter.woff2", "gallery.html"} {
		if !names[want] {
			t.Errorf("bundle misses %s (got %v)", want, names)
		}
	}
	if names["shots/one.png"] && names["shots/two.png"] {
		t.Errorf("bundle holds both screenshots, over the cap: %v", names)
	}
}

// A page whose own files exceed the cap fails with a message naming them.
func TestFileBundlePageTooLargeNamesBiggestFiles(t *testing.T) {
	const limit = 64 << 10
	folder := writeBundleTree(t, map[string]string{"index.html": `<img src="shots/huge.png">`}, map[string]int{"shots/huge.png": 80 << 10})
	var out bytes.Buffer
	err := writeFileBundle(&out, filepath.Join(folder, "index.html"), limit)
	if err == nil {
		t.Fatal("accepted a page whose assets exceed the cap")
	}
	if !strings.Contains(err.Error(), "shots/huge.png") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("error does not name the biggest file and the limit: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("rejected bundle emitted %d bytes", out.Len())
	}
}

// A folder over the cap names its biggest files instead of a bare limit error.
func TestFileBundleFolderTooLargeNamesBiggestFiles(t *testing.T) {
	const limit = 64 << 10
	folder := writeBundleTree(t, map[string]string{"index.html": "hi"}, map[string]int{"shots/a.png": 40 << 10, "shots/b.png": 30 << 10})
	var out bytes.Buffer
	err := writeFileBundle(&out, folder, limit)
	if err == nil {
		t.Fatal("accepted a folder over the cap")
	}
	for _, want := range []string{"shots/a.png", "shots/b.png", "limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error misses %q: %v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("rejected bundle emitted %d bytes", out.Len())
	}
}

// A link (<a href>) to a heavy sibling page is navigation, not something the
// page loads: the page still opens when that sibling's screenshots exceed the cap.
func TestFileBundlePageLinkToHeavyPageIsNotRequired(t *testing.T) {
	const limit = 64 << 10
	folder := writeBundleTree(t, map[string]string{
		"index.html":   `<a href="gallery.html">gallery</a><area href="gallery.html"><link rel="stylesheet" href="app.css">`,
		"app.css":      `body{}`,
		"gallery.html": `<img src="shots/one.png"><img src="shots/two.png">`,
	}, map[string]int{"shots/one.png": 40 << 10, "shots/two.png": 40 << 10})
	var out bytes.Buffer
	if err := writeFileBundle(&out, filepath.Join(folder, "index.html"), limit); err != nil {
		t.Fatalf("page linking to a heavy page failed: %v", err)
	}
	names := bundleNames(t, out.Bytes())
	for _, want := range []string{"index.html", "app.css", "gallery.html"} {
		if !names[want] {
			t.Errorf("bundle misses %s: %v", want, names)
		}
	}
}
