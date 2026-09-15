// dist-local builds release-shaped archives without publishing a release.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type platform struct{ os, arch string }

var platforms = []platform{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// Numeric prerelease identifiers cannot have leading zeroes in SemVer.
const prereleaseIdentifier = `(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(-` + prereleaseIdentifier + `(\.` + prereleaseIdentifier + `)*)?` +
	`(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func main() {
	version := flag.String("version", "", "version to embed (default: latest git tag +local.UTC-date.sha)")
	out := flag.String("output", "dist-local", "directory for archives and checksums.txt")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %v", flag.Args()))
	}
	if *version == "" {
		var err error
		*version, err = defaultVersion(time.Now())
		if err != nil {
			fatal(err)
		}
	}
	if err := distribute(*version, *out, buildBinary); err != nil {
		fatal(err)
	}
	fmt.Printf("Built %s in %s\n", *version, *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dist-local:", err)
	os.Exit(1)
}

func defaultVersion(now time.Time) (string, error) {
	tag, err := exec.Command("git", "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*").Output()
	if err != nil {
		return "", fmt.Errorf("find version tag (or supply -version): %w", err)
	}
	sha, err := exec.Command("git", "rev-parse", "--short=8", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read commit: %w", err)
	}
	base := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(string(tag)), "v"), "+", 2)[0]
	return base + "+local." + now.UTC().Format("20060102") + "." + strings.TrimSpace(string(sha)), nil
}

func buildBinary(target platform, version, destination string) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.Version="+version, "-o", destination, "./cmd/agent-deck")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func distribute(version, output string, build func(platform, string, string) error) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid version %q: expected a semantic version", version)
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(output, ".build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	var checksums strings.Builder
	for _, target := range platforms {
		binary := filepath.Join(staging, "agent-deck")
		if err := build(target, version, binary); err != nil {
			return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
		}
		name := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, target.os, target.arch)
		path := filepath.Join(staging, name)
		if err := writeArchive(path, binary); err != nil {
			return err
		}
		archive, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, archive)
		closeErr := archive.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&checksums, "%x  %s\n", hash.Sum(nil), name)
	}
	if err := os.WriteFile(filepath.Join(staging, "checksums.txt"), []byte(checksums.String()), 0644); err != nil {
		return err
	}
	// Publish checksums last so an interrupted build never advertises incomplete archives.
	for _, target := range platforms {
		name := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, target.os, target.arch)
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(output, name)); err != nil {
			return err
		}
	}
	return os.Rename(filepath.Join(staging, "checksums.txt"), filepath.Join(output, "checksums.txt"))
}

func writeArchive(destination, binary string) (err error) {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	gz := gzip.NewWriter(file)
	defer func() {
		if closeErr := gz.Close(); err == nil {
			err = closeErr
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if closeErr := tw.Close(); err == nil {
			err = closeErr
		}
	}()
	for _, item := range []struct {
		name, path string
		mode       int64
	}{
		{"agent-deck", binary, 0755}, {"README.md", "README.md", 0644},
		{"LICENSE", "LICENSE", 0644}, {"CHANGELOG.md", "CHANGELOG.md", 0644},
	} {
		if err := addFile(tw, item.name, item.path, item.mode); err != nil {
			return err
		}
	}
	return nil
}

func addFile(tw *tar.Writer, name, path string, mode int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, file)
	return err
}
