package session

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"debug/macho"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// LocalBuild is a release-layout directory with one version and its checksums.
// The directory is only used on the controller; it is never sent over SSH.
type LocalBuild struct {
	Version   string
	dir       string
	checksums map[string]string
}

var localArchiveName = regexp.MustCompile(`^agent-deck_(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)_(darwin|linux)_(amd64|arm64)\.tar\.gz$`)

func LoadLocalBuild(dir string) (*LocalBuild, error) {
	data, err := os.ReadFile(filepath.Join(dir, update.ChecksumsAssetName))
	if err != nil {
		return nil, fmt.Errorf("read local build checksums: %w", err)
	}
	build := &LocalBuild{dir: dir, checksums: update.ParseChecksums(data)}
	for name := range build.checksums {
		match := localArchiveName.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("unexpected local build checksum entry %q", name)
		}
		if build.Version != "" && build.Version != match[1] {
			return nil, fmt.Errorf("local build contains multiple versions; use a directory for one build")
		}
		build.Version = match[1]
	}
	if build.Version == "" {
		return nil, fmt.Errorf("local build has no platform archives in checksums.txt")
	}
	return build, nil
}

func (b *LocalBuild) archive(goos, goarch string) ([]byte, string, error) {
	name := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", b.Version, goos, goarch)
	if !localArchiveName.MatchString(name) {
		return nil, "", fmt.Errorf("unsupported platform %s/%s", goos, goarch)
	}
	sum, ok := b.checksums[name]
	if !ok {
		return nil, "", fmt.Errorf("local build has no archive for %s/%s", goos, goarch)
	}
	data, err := os.ReadFile(filepath.Join(b.dir, name))
	if err != nil {
		return nil, "", fmt.Errorf("read local archive: %w", err)
	}
	// Share the release integrity gate without modifying its behavior.
	if err := update.VerifyAssetChecksum(name, data, b.checksums); err != nil {
		return nil, "", err
	}
	if err := validateLocalArchive(data, goos, goarch); err != nil {
		return nil, "", err
	}
	return data, sum, nil
}

type localArchiveInstaller interface {
	InstallLocalArchiveWithForce(context.Context, []byte, string, string, bool, bool) error
}

type installPreviewer interface {
	PreviewInstallWithForce(context.Context, string, bool) error
}

func deployLocalBuild(ctx context.Context, runner RemoteBinaryInstaller, opts RemoteUpdateOptions) (string, error) {
	build := opts.LocalBuild
	if isVersionString(opts.CurrentVersion) && update.CompareVersions(opts.CurrentVersion, build.Version) > 0 && !opts.Force {
		return "", fmt.Errorf("refusing downgrade from v%s to v%s; use --force", opts.CurrentVersion, build.Version)
	}
	goos, goarch, err := runner.DetectPlatform(ctx)
	if err != nil {
		return "", err
	}
	opts.Progress(fmt.Sprintf("Local build v%s for %s/%s", build.Version, goos, goarch))
	archive, checksum, err := build.archive(goos, goarch)
	if err != nil {
		return "", err
	}
	installer, ok := runner.(localArchiveInstaller)
	if !ok {
		return "", fmt.Errorf("SSH runner does not support local archives")
	}
	if err := installer.InstallLocalArchiveWithForce(ctx, archive, checksum, build.Version, opts.DryRun, opts.Force); err != nil {
		return "", err
	}
	return build.Version, nil
}

// A local build of the same release core must yield to that published release.
// Metadata has no semver precedence; provenance resolves the equal-version case.
func localBuildNeedsRelease(state RemoteVersionState, target string) bool {
	return (state.InstalledFrom == "local-build" || strings.Contains(state.Version, "+local.")) && !strings.Contains(target, "+") &&
		isVersionString(target) && update.CompareVersions(state.Version, target) == 0
}

// Inspect the executable without running controller-side code from the archive.
func validateLocalArchive(data []byte, goos, goarch string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid local archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid local archive: %w", err)
		}
		switch header.Name {
		case "README.md", "LICENSE", "CHANGELOG.md":
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("archive documentation must be regular files")
			}
			continue
		case "agent-deck":
		default:
			return fmt.Errorf("unexpected local archive entry %q", header.Name)
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 512<<20 {
			return fmt.Errorf("archive must contain one regular agent-deck executable")
		}
		found = true
		binary, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		switch goos {
		case "linux":
			file, err := elf.NewFile(bytes.NewReader(binary))
			if err != nil {
				return fmt.Errorf("local archive does not contain a Linux executable: %w", err)
			}
			expected := elf.EM_X86_64
			if goarch == "arm64" {
				expected = elf.EM_AARCH64
			}
			if file.Machine != expected || file.Class != elf.ELFCLASS64 {
				return fmt.Errorf("local archive executable does not match %s/%s", goos, goarch)
			}
		case "darwin":
			file, err := macho.NewFile(bytes.NewReader(binary))
			if err != nil {
				return fmt.Errorf("local archive does not contain a Darwin executable: %w", err)
			}
			expected := macho.CpuAmd64
			if goarch == "arm64" {
				expected = macho.CpuArm64
			}
			if file.Cpu != expected {
				return fmt.Errorf("local archive executable does not match %s/%s", goos, goarch)
			}
		}
	}
	if !found {
		return fmt.Errorf("local archive has no agent-deck executable")
	}
	return nil
}
