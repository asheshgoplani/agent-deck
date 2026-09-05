package session

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These flags exist only in the test binary. Re-exec children reuse the exact
// CLI built by their parent; runtime HOME remains independently isolated.
var (
	inheritedTestHelper        = flag.String("session-test-completion-helper", "", "parent-owned completion CLI")
	inheritedTestHelperDigest  = flag.String("session-test-completion-helper-sha256", "", "SHA-256 of parent-owned CLI")
	completionTestHelperDigest string
)

func validateSessionTestHelper(path, digest string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("helper path must be absolute")
	}
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size {
		return fmt.Errorf("helper digest must be SHA-256")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat inherited helper: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("helper must be a regular executable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read inherited helper: %w", err)
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != digest {
		return fmt.Errorf("inherited helper digest mismatch")
	}
	return nil
}

func provisionSessionTestHelper() (func(), error) {
	inherited := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "session-test-completion-helper" || f.Name == "session-test-completion-helper-sha256" {
			inherited = true
		}
	})
	if inherited {
		if err := validateSessionTestHelper(*inheritedTestHelper, *inheritedTestHelperDigest); err != nil {
			return nil, err
		}
		completionExecutableForTests = *inheritedTestHelper
		completionTestHelperDigest = *inheritedTestHelperDigest
		return func() {}, nil // The parent owns this helper until all children exit.
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	projectRoot := filepath.Clean(filepath.Join(root, "../.."))
	if _, err := os.Stat(filepath.Join(projectRoot, "cmd", "agent-deck")); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "agent-deck-test-helper-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	out := filepath.Join(dir, "agent-deck")
	cmd := exec.Command("go", "build", "-p", "1", "-o", out, "./cmd/agent-deck")
	cmd.Dir = projectRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("build matching CLI: %w: %s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Chmod(out, 0555); err != nil {
		cleanup()
		return nil, err
	}
	digest := sha256.Sum256(data)
	completionExecutableForTests = out
	completionTestHelperDigest = hex.EncodeToString(digest[:])
	return cleanup, nil
}

func sessionTestChildCommand(t *testing.T, testName string) *exec.Cmd {
	t.Helper()
	if err := validateSessionTestHelper(completionExecutableForTests, completionTestHelperDigest); err != nil {
		t.Fatal(err)
	}
	return exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(testName)+"$",
		"-session-test-completion-helper="+completionExecutableForTests,
		"-session-test-completion-helper-sha256="+completionTestHelperDigest)
}

func TestSessionTestHelperChild(t *testing.T) {
	if *inheritedTestHelper == "" {
		return
	}
	if completionExecutableForTests != *inheritedTestHelper || completionTestHelperDigest != *inheritedTestHelperDigest {
		t.Fatal("child did not retain the parent helper identity")
	}
	if err := validateSessionTestHelper(completionExecutableForTests, completionTestHelperDigest); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTestHelperInheritance(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "go-was-invoked")
	// Any child build is an explicit failure, even with a warm Go cache.
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf invoked > \"$SESSION_TEST_GO_MARKER\"\nexit 97\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SESSION_TEST_GO_MARKER", marker)
	nonexec := filepath.Join(bin, "non-executable")
	if err := os.WriteFile(nonexec, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, path, digest, want string }{
		{"same-parent-helper", completionExecutableForTests, completionTestHelperDigest, ""},
		{"missing-path", "", completionTestHelperDigest, "helper path must be absolute"},
		{"empty-path-only", "", "", "helper path must be absolute"},
		{"empty-digest-only", "", "", "helper path must be absolute"},
		{"both-empty", "", "", "helper path must be absolute"},
		{"missing-digest", completionExecutableForTests, "", "helper digest must be SHA-256"},
		{"relative-path", "agent-deck", completionTestHelperDigest, "helper path must be absolute"},
		{"missing-file", filepath.Join(bin, "missing"), completionTestHelperDigest, "stat inherited helper"},
		{"directory", bin, completionTestHelperDigest, "helper must be a regular executable"},
		{"non-executable", nonexec, completionTestHelperDigest, "helper must be a regular executable"},
		{"wrong-digest", completionExecutableForTests, strings.Repeat("0", 64), "inherited helper digest mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := sessionTestChildCommand(t, "TestSessionTestHelperChild")
			cmd.Args[2] = "-session-test-completion-helper=" + tt.path
			cmd.Args[3] = "-session-test-completion-helper-sha256=" + tt.digest
			if tt.name == "empty-path-only" {
				cmd.Args = cmd.Args[:3]
			} else if tt.name == "empty-digest-only" {
				cmd.Args = append(cmd.Args[:2], cmd.Args[3])
			}
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("inherited child failed: %v: %s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tt.want) {
				t.Fatalf("invalid inheritance: error=%v output=%s; want %q", err, output, tt.want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("child invoked Go: %v", err)
			}
		})
	}
	// No child may remove the shared helper, including rejected children.
	if err := validateSessionTestHelper(completionExecutableForTests, completionTestHelperDigest); err != nil {
		t.Fatal(err)
	}
}
