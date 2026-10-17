package main

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBundleRelativeAssets(t *testing.T) {
	folder := t.TempDir()
	if err := os.Mkdir(filepath.Join(folder, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"report.html": `<img src="assets/image.svg">`, "assets/image.svg": "<svg/>"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{folder, filepath.Join(folder, "report.html")} {
		var out bytes.Buffer
		if err := writeFileBundle(&out, target, fileBundleMaxBytes); err != nil {
			t.Fatal(err)
		}
		archive := tar.NewReader(&out)
		found := map[string]string{}
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Typeflag == tar.TypeDir {
				continue
			}
			data, err := io.ReadAll(archive)
			if err != nil {
				t.Fatal(err)
			}
			found[header.Name] = string(data)
		}
		for name, data := range files {
			if found[name] != data {
				t.Errorf("%s: file %s = %q", target, name, found[name])
			}
		}
		if len(found) != len(files) {
			t.Errorf("unexpected files: %v", found)
		}
	}
}

func TestFileBundleCapIncludesArchiveOverhead(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "report.html"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	var full bytes.Buffer
	if err := writeFileBundle(&full, folder, fileBundleMaxBytes); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{4, int64(full.Len() - 1)} {
		var out bytes.Buffer
		err := writeFileBundle(&out, folder, limit)
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("limit %d: %v", limit, err)
		}
		if out.Len() != 0 {
			t.Errorf("rejected bundle emitted %d bytes", out.Len())
		}
	}
	var exact bytes.Buffer
	if err := writeFileBundle(&exact, folder, int64(full.Len())); err != nil {
		t.Fatalf("exact cap: %v", err)
	}
}

func TestFileBundleRefusesSymlinks(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.html")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, directoryLink := range []bool{false, true} {
		folder := t.TempDir()
		target := secret
		if directoryLink {
			target = outside
		}
		link := filepath.Join(folder, "escape")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		for _, requested := range []string{folder, link} {
			var out bytes.Buffer
			if err := writeFileBundle(&out, requested, fileBundleMaxBytes); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("%s: %v", requested, err)
			}
			if out.Len() != 0 {
				t.Error("symlink refusal emitted archive bytes")
			}
		}
	}
}

func TestFileBundleOversizeSparseFile(t *testing.T) {
	folder := t.TempDir()
	f, err := os.Create(filepath.Join(folder, "large.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(fileBundleMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeFileBundle(&out, folder, fileBundleMaxBytes); err == nil {
		t.Fatal("accepted oversized file")
	}
	if out.Len() != 0 {
		t.Fatal("oversized bundle emitted bytes")
	}
}

func TestFileBundleRequiresExplicitSession(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if err := runFileBundle("default", []string{"bundle", t.TempDir()}, &out, &diagnostic); err == nil {
		t.Fatal("accepted missing session")
	}
	if out.Len() != 0 {
		t.Fatal("invalid request emitted archive")
	}
}

func TestFileBundleRefusesInternalSymlink(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "report.html"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("report.html", filepath.Join(folder, "alias.html")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeFileBundle(&out, folder, fileBundleMaxBytes); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("internal symlink: %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("rejected bundle emitted bytes")
	}
}

func TestFileBundleDoesNotInitializeCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("AGENT_DECK_HOME", filepath.Join(home, ".agent-deck"))
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "report.html"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := runFileBundle("uninitialized-profile", []string{"bundle", folder, "--session", "routing-session"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(&out)
	header, err := archive.Next()
	if err != nil || header.Name != "report.html" {
		t.Fatalf("bundle header = %v, %v", header, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read-only bundle created home entries: %v", entries)
	}
	entries, err = os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.html" {
		t.Fatalf("bundle changed source entries: %v", entries)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", &diagnostics)
	}
}
