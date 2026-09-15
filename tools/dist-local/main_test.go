package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDistributeReleaseLayout(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, name := range []string{"README.md", "LICENSE", "CHANGELOG.md"} {
		if err := os.WriteFile(name, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const version = "1.16.10+local.20260915.deadbeef"
	var built []platform
	err := distribute(version, "dist", func(target platform, gotVersion, path string) error {
		if gotVersion != version {
			t.Fatalf("version = %s", gotVersion)
		}
		built = append(built, target)
		return os.WriteFile(path, []byte(target.os+"/"+target.arch), 0755)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 3 {
		t.Fatalf("builds = %v", built)
	}
	checksums, err := os.ReadFile("dist/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	var wantChecksums strings.Builder
	for _, target := range []platform{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, target.os, target.arch)
		content, err := os.ReadFile(filepath.Join("dist", name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&wantChecksums, "%x  %s\n", sha256.Sum256(content), name)
		gz, err := gzip.NewReader(bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		expected := map[string]string{"agent-deck": target.os + "/" + target.arch, "README.md": "README.md", "LICENSE": "LICENSE", "CHANGELOG.md": "CHANGELOG.md"}
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			want, ok := expected[header.Name]
			if !ok {
				t.Fatalf("unexpected archive member %q", header.Name)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != want {
				t.Fatalf("%s content = %q", header.Name, body)
			}
			if header.Name == "agent-deck" && header.Mode != 0755 {
				t.Fatalf("binary mode = %o", header.Mode)
			}
			delete(expected, header.Name)
		}
		if len(expected) != 0 {
			t.Fatalf("missing files: %v", expected)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if string(checksums) != wantChecksums.String() {
		t.Fatalf("checksums mismatch: %s", checksums)
	}
	entries, err := os.ReadDir("dist")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("unexpected output: %v", entries)
	}
}

func TestFailedBuildPreservesPublishedDistribution(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("dist", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("dist/checksums.txt", []byte("previous manifest"), 0644); err != nil {
		t.Fatal(err)
	}
	err := distribute("1.16.10+local.test", "dist", func(platform, string, string) error { return errors.New("build failed") })
	if err == nil {
		t.Fatal("expected build error")
	}
	manifest, err := os.ReadFile("dist/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(manifest) != "previous manifest" {
		t.Fatalf("manifest changed: %q", manifest)
	}
	entries, err := os.ReadDir("dist")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("staging leaked: %v", entries)
	}
}

func TestInvalidVersionRejectedBeforeBuild(t *testing.T) {
	for _, version := range []string{"../outside", "1.2.3 -X main.Other=oops", "v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3-rc.01"} {
		t.Run(version, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "dist")
			err := distribute(version, output, func(platform, string, string) error { t.Fatal("unexpected build"); return nil })
			if err == nil {
				t.Fatal("invalid version accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("output created: %v", err)
			}
		})
	}
}

func TestDefaultVersionUsesTagUTCDateAndCommit(t *testing.T) {
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "fixture")
	git("tag", "v1.16.10")
	sha := git("rev-parse", "--short=8", "HEAD")
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	got, err := defaultVersion(now)
	if err != nil {
		t.Fatal(err)
	}
	want := "1.16.10+local.20260915." + sha
	if got != want {
		t.Fatalf("version = %s, want %s", got, want)
	}
}
