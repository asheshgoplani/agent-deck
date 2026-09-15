package session

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

type localBuildRunner struct {
	version, arch      string
	installs, previews int
}

func (r *localBuildRunner) CheckBinary(context.Context) (string, bool) { return r.version, true }
func (r *localBuildRunner) DetectPlatform(context.Context) (string, string, error) {
	return "linux", r.arch, nil
}
func (r *localBuildRunner) InstallBinary(_ context.Context, _ []byte, version string) error {
	r.installs++
	r.version = version
	return nil
}
func (r *localBuildRunner) InstallLocalArchiveWithForce(_ context.Context, _ []byte, _, version string, dry, force bool) error {
	if dry {
		r.previews++
	} else {
		r.installs++
		r.version = version
	}
	return nil
}
func (r *localBuildRunner) LastInstallReport() string { return "destination /opt/bin/agent-deck" }

func testLocalBuild(t *testing.T) *LocalBuild {
	t.Helper()
	dir := t.TempDir()
	version := "1.16.10+local.20260915.abc"
	data, digest := localELFArchive(t, 62)
	name := "agent-deck_" + version + "_linux_amd64.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(digest+"  "+name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	build, err := LoadLocalBuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	return build
}

func TestUpdateRemotes_LocalBuild(t *testing.T) {
	for _, tc := range []struct {
		name, version, arch string
		force, dry          bool
		outcome             RemoteUpdateOutcome
		installs, previews  int
	}{
		{"happy", "1.16.9", "amd64", false, false, RemoteUpdateOutcomeUpdated, 1, 0},
		{"same core", "1.16.10", "amd64", false, false, RemoteUpdateOutcomeUpdated, 1, 0},
		{"wrong arch", "1.16.9", "arm64", false, false, RemoteUpdateOutcomeFailed, 0, 0},
		{"downgrade refused", "1.17.0", "amd64", false, false, RemoteUpdateOutcomeFailed, 0, 0},
		{"forced downgrade", "1.17.0", "amd64", true, false, RemoteUpdateOutcomeUpdated, 1, 0},
		{"dry run", "1.16.9", "amd64", false, true, RemoteUpdateOutcomeSkipped, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSessionXDGPathEnv(t)
			build := testLocalBuild(t)
			runner := &localBuildRunner{version: tc.version, arch: tc.arch}
			opts := RemoteUpdateOptions{LocalBuild: build, Force: tc.force, DryRun: tc.dry, NewRunner: func(string, RemoteConfig) RemoteBinaryInstaller { return runner }}
			results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"box": {Host: "fake"}}, "1.16.0", opts)
			if len(results) != 1 || results[0].Outcome != tc.outcome || runner.installs != tc.installs || runner.previews != tc.previews {
				t.Fatalf("results=%+v runner=%+v", results, runner)
			}
			cache := LoadRemoteVersions()
			if tc.dry {
				if len(cache) != 0 {
					t.Fatalf("dry run wrote cache: %+v", cache)
				}
			} else if tc.installs > 0 {
				state := cache["box"]
				if state.InstalledFrom != "local-build" || state.Version != build.Version {
					t.Fatalf("cache %+v", state)
				}
			}
		})
	}
}

func TestUpdateRemotes_LocalChecksumMismatchNeverInstalls(t *testing.T) {
	setupSessionXDGPathEnv(t)
	build := testLocalBuild(t)
	for name := range build.checksums {
		if err := os.WriteFile(filepath.Join(build.dir, name), []byte("corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &localBuildRunner{version: "1.16.9", arch: "amd64"}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"box": {}}, "ignored", RemoteUpdateOptions{LocalBuild: build, NewRunner: func(string, RemoteConfig) RemoteBinaryInstaller { return runner }})
	if results[0].Outcome != RemoteUpdateOutcomeFailed || !strings.Contains(fmt.Sprint(results[0].Err), "SHA-256 mismatch") || runner.installs != 0 || runner.version != "1.16.9" {
		t.Fatalf("result %+v runner %+v", results, runner)
	}
}

func TestLocalBuildReleaseSweepAndProvenance(t *testing.T) {
	setupSessionXDGPathEnv(t)
	state := RemoteVersionState{Version: "1.16.10+local.20260915.abc", Found: true, InstalledFrom: "local-build", CheckedAt: time.Now()}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"box": state}); err != nil {
		t.Fatal(err)
	}
	observed := state
	observed.InstalledFrom = ""
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"box": observed}); err != nil {
		t.Fatal(err)
	}
	if got := LoadRemoteVersions()["box"].InstalledFrom; got != "local-build" {
		t.Fatal(got)
	}
	for _, target := range []string{"1.16.10", "1.16.11"} {
		if !state.Outdated(target) || PlanRemoteUpdates(map[string]RemoteVersionState{"box": state}, target)[0].Kind != RemoteUpdateUpgrade {
			t.Fatal("local build not replaced by release", target)
		}
	}
	if state.Outdated("1.16.9") {
		t.Fatal("local build downgrades in sweep")
	}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"box": {Found: false}}); err != nil {
		t.Fatal(err)
	}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"box": observed}); err != nil {
		t.Fatal(err)
	}
	if LoadRemoteVersions()["box"].InstalledFrom != "local-build" {
		t.Fatal("transient probe lost provenance")
	}
	runner := &localBuildRunner{version: state.Version, arch: "amd64"}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"box": {}}, "1.16.10", RemoteUpdateOptions{
		NewRunner:    func(string, RemoteConfig) RemoteBinaryInstaller { return runner },
		FetchRelease: func(string) (*update.Release, error) { return &update.Release{TagName: "v1.16.10"}, nil },
		Download:     func(*update.Release, string, string) ([]byte, error) { return []byte("verified release"), nil },
	})
	if results[0].Outcome != RemoteUpdateOutcomeUpdated || runner.version != "1.16.10" || LoadRemoteVersions()["box"].InstalledFrom != "release" {
		t.Fatalf("results %+v cache %+v", results, LoadRemoteVersions())
	}
}

func TestLoadLocalBuildRejectsAmbiguousManifest(t *testing.T) {
	for _, manifest := range []string{"", "abc  ../../secret\n", "abc  agent-deck_1.0.0_linux_amd64.tar.gz\ndef  agent-deck_2.0.0_linux_arm64.tar.gz\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadLocalBuild(dir); err == nil {
			t.Fatal("accepted ambiguous/invalid manifest", manifest)
		}
	}
}

func localELFArchive(t *testing.T, machine uint16) ([]byte, string) {
	t.Helper()
	payload := make([]byte, 64)
	copy(payload, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(payload[16:], 2)
	binary.LittleEndian.PutUint16(payload[18:], machine)
	binary.LittleEndian.PutUint32(payload[20:], 1)
	binary.LittleEndian.PutUint16(payload[52:], 64)
	binary.LittleEndian.PutUint16(payload[54:], 56)
	binary.LittleEndian.PutUint16(payload[58:], 64)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "agent-deck", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	return data, fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestLocalBuildRejectsMislabeledArchitecture(t *testing.T) {
	build := testLocalBuild(t)
	data, digest := localELFArchive(t, 183)
	for name := range build.checksums {
		build.checksums[name] = digest
		if err := os.WriteFile(filepath.Join(build.dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := build.archive("linux", "amd64"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mislabeled archive accepted: %v", err)
	}
}

func TestUpdateRemotes_LocalBuildAllContinuesAfterFailure(t *testing.T) {
	setupSessionXDGPathEnv(t)
	build := testLocalBuild(t)
	runners := map[string]*localBuildRunner{
		"a-wrong-arch": {version: "1.16.9", arch: "arm64"},
		"b-success":    {version: "1.16.9", arch: "amd64"},
	}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"a-wrong-arch": {}, "b-success": {}}, "ignored", RemoteUpdateOptions{LocalBuild: build, NewRunner: func(name string, _ RemoteConfig) RemoteBinaryInstaller { return runners[name] }})
	if len(results) != 2 || results[0].Name != "a-wrong-arch" || results[0].Outcome != RemoteUpdateOutcomeFailed || results[1].Name != "b-success" || results[1].Outcome != RemoteUpdateOutcomeUpdated {
		t.Fatalf("results %+v", results)
	}
}

func TestRemoteLocalPrereleaseVersion(t *testing.T) {
	version := "1.17.0-rc.1+local.20260915.abc"
	if got := parseRemoteVersion("Agent Deck v" + version); got != version {
		t.Fatalf("got %q", got)
	}
	if !isVersionString(version) {
		t.Fatal("prerelease plus metadata rejected")
	}
}
