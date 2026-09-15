package session

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localArchiveFixture(t *testing.T, version string) ([]byte, string) {
	t.Helper()
	return localArchivePayload(t, []byte(fakeAgentDeckPayload(version)))
}

func localArchivePayload(t *testing.T, payload []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "agent-deck", Mode: 0755, Size: int64(len(payload))}); err != nil {
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

func TestInstallLocalArchive_ChecksumMismatchPreservesOldBinary(t *testing.T) {
	l := newRemoteLayout(t)
	target := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, target, "1.16.5", 0755)
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	r := l.runner(t, target, noSudo)
	data, _ := localArchiveFixture(t, "1.16.6+local.test")
	err = r.InstallLocalArchive(context.Background(), data, strings.Repeat("0", 64), "1.16.6+local.test", false)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("checksum failure replaced old binary inode")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != fakeAgentDeckPayload("1.16.5") {
		t.Fatalf("old binary changed: %q, %v", got, err)
	}
	entries, err := os.ReadDir(l.pathDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging files left after failure: %v, %v", entries, err)
	}
}

func TestInstallLocalArchive_SymlinkAndDryRun(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dryRun=%v", dryRun), func(t *testing.T) {
			l := newRemoteLayout(t)
			target := filepath.Join(l.home, "bin with spaces", "agent-deck")
			fakeAgentDeck(t, target, "1.16.5", 0750)
			link := filepath.Join(l.pathDir, "agent-deck")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			r := l.runner(t, link, noSudo)
			originalExec := r.remoteExecFn
			streams := 0
			r.remoteExecFn = func(ctx context.Context, command string, stdin []byte) ([]byte, error) {
				if stdin != nil {
					streams++
				}
				return originalExec(ctx, command, stdin)
			}
			data, digest := localArchiveFixture(t, "1.16.6+local.test")
			if err := r.InstallLocalArchive(context.Background(), data, digest, "1.16.6+local.test", dryRun); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink changed: %v, %v", info, err)
			}
			want := "1.16.6+local.test"
			if dryRun {
				want = "1.16.5"
				if streams != 0 {
					t.Fatal("dry run uploaded archive")
				}
			} else if streams != 1 {
				t.Fatalf("stream count: %d", streams)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != fakeAgentDeckPayload(want) {
				t.Fatalf("binary: %q, %v", got, err)
			}
			if !strings.Contains(r.LastInstallReport(), target) {
				t.Fatalf("report lacks target: %s", r.LastInstallReport())
			}
			if dryRun && !strings.Contains(r.LastInstallReport(), "would deploy") {
				t.Fatal(r.LastInstallReport())
			}
			mustSameFile(t, link, target)
		})
	}
}

func TestInstallLocalArchive_InvalidChecksumDoesNotContactRemote(t *testing.T) {
	r := &SSHRunner{remoteExecFn: func(context.Context, string, []byte) ([]byte, error) {
		t.Fatal("invalid checksum reached remote")
		return nil, nil
	}}
	if err := r.InstallLocalArchive(context.Background(), nil, "bad'checksum", "1.16.6", false); err == nil {
		t.Fatal("accepted invalid checksum")
	}
}

func TestInstallLocalArchive_StagedVersionMismatchPreservesOldBinary(t *testing.T) {
	l := newRemoteLayout(t)
	target := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, target, "1.16.5", 0755)
	before, _ := os.Stat(target)
	data, digest := localArchiveFixture(t, "1.16.6+local.other")
	r := l.runner(t, target, noSudo)
	err := r.InstallLocalArchive(context.Background(), data, digest, "1.16.6+local.expected", false)
	if err == nil || !strings.Contains(err.Error(), "staged binary version mismatch") {
		t.Fatalf("want staged version mismatch, got %v", err)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("version failure replaced old binary")
	}
}

func TestInstallLocalArchive_DistinctPATHWithSameCoreVersion(t *testing.T) {
	for _, existing := range []string{"1.16.6", "1.16.6+local.older"} {
		t.Run(existing, func(t *testing.T) {
			l := newRemoteLayout(t)
			configured := filepath.Join(l.home, "bin", "agent-deck")
			onPath := filepath.Join(l.pathDir, "agent-deck")
			fakeAgentDeck(t, configured, existing, 0755)
			fakeAgentDeck(t, onPath, existing, 0755)
			r := l.runner(t, configured, noSudo)
			want := "1.16.6+local.new"
			data, digest := localArchiveFixture(t, want)
			if err := r.InstallLocalArchive(context.Background(), data, digest, want, false); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{configured, onPath} {
				got, _ := os.ReadFile(path)
				if string(got) != fakeAgentDeckPayload(want) {
					t.Fatalf("%s was not updated: %q", path, got)
				}
			}
		})
	}
}

func TestInstallLocalArchive_ForceUpdatesNewerPATHBinary(t *testing.T) {
	l := newRemoteLayout(t)
	configured := filepath.Join(l.home, "bin", "agent-deck")
	onPath := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, configured, "1.16.9", 0755)
	fakeAgentDeck(t, onPath, "1.16.9", 0755)
	r := l.runner(t, configured, noSudo)
	want := "1.16.6+local.test"
	data, digest := localArchiveFixture(t, want)
	if err := r.InstallLocalArchiveWithForce(context.Background(), data, digest, want, false, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{configured, onPath} {
		got, _ := os.ReadFile(path)
		if string(got) != fakeAgentDeckPayload(want) {
			t.Fatalf("%s was not updated: %q", path, got)
		}
	}
}

func TestInstallLocalArchive_UnexecutableBinaryPreservesOldBinary(t *testing.T) {
	l := newRemoteLayout(t)
	target := filepath.Join(l.pathDir, "agent-deck")
	fakeAgentDeck(t, target, "1.16.5", 0755)
	before, _ := os.Stat(target)
	data, digest := localArchivePayload(t, []byte("#!/nonexistent-architecture-interpreter\n"))
	r := l.runner(t, target, noSudo)
	err := r.InstallLocalArchive(context.Background(), data, digest, "1.16.6+local.test", false)
	if err == nil || !strings.Contains(err.Error(), "staged binary cannot execute") {
		t.Fatalf("want staged execution failure, got %v", err)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("execution failure replaced old binary")
	}
}

func TestInstallLocalArchive_RejectsAmbiguousOrLinkedBinary(t *testing.T) {
	for _, kind := range []string{"duplicate", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			header := &tar.Header{Name: "agent-deck", Mode: 0755}
			if kind == "symlink" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/bin/sh"
			}
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate" {
				if err := tw.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			digest := fmt.Sprintf("%x", sha256.Sum256(data))
			l := newRemoteLayout(t)
			target := filepath.Join(l.pathDir, "agent-deck")
			fakeAgentDeck(t, target, "1.16.5", 0755)
			before, _ := os.Stat(target)
			r := l.runner(t, target, noSudo)
			if err := r.InstallLocalArchive(context.Background(), data, digest, "1.16.6", false); err == nil {
				t.Fatal("accepted invalid archive binary")
			}
			after, _ := os.Stat(target)
			if !os.SameFile(before, after) {
				t.Fatal("invalid archive replaced old binary")
			}
		})
	}
}
