// issue1206_install_checksum_test.go pins the supply-chain hardening for
// install.sh (audit H1, sec-secrets-REPORT.md). The documented entry point is
// `curl -fsSL .../install.sh | bash`, which downloads a release tarball and runs
// the binary inside it. Pre-fix the installer extracted and installed the
// download with NO integrity check: a tampered release asset, an account/token
// compromise, or a CDN/MITM swap executes arbitrary code on every installing
// machine. The fix fetches the release `checksums.txt` and `sha256sum -c`-style
// verifies the tarball, aborting BEFORE extraction on any mismatch/missing entry.
//
// Two invariants guard it:
//  1. Structural: install.sh fetches checksums.txt and calls the verifier
//     before `tar -xzf`, and aborts (exit 1) on failure.
//  2. Functional: the verify_download_checksum shell function (sourced in
//     isolation) returns success only on an exact SHA-256 match.
package releasetests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func installScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "install.sh")
}

func installScriptBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(installScriptPath(t))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	return string(raw)
}

// TestInstallScript_VerifiesChecksumBeforeExtract asserts install.sh fetches
// checksums.txt and verifies the download before the `tar -xzf` extraction.
func TestInstallScript_VerifiesChecksumBeforeExtract(t *testing.T) {
	body := installScriptBody(t)

	if !strings.Contains(body, "checksums.txt") {
		t.Fatalf("install.sh never fetches checksums.txt — downloaded asset is unverified (H1)")
	}
	if !regexp.MustCompile(`verify_download_checksum`).MatchString(body) {
		t.Fatalf("install.sh has no verify_download_checksum step (H1)")
	}

	verifyIdx := strings.Index(body, "verify_download_checksum")
	extractIdx := strings.Index(body, "tar -xzf")
	if extractIdx < 0 {
		t.Fatalf("install.sh no longer extracts with `tar -xzf` — update this test")
	}
	if verifyIdx < 0 || verifyIdx > extractIdx {
		t.Fatalf("checksum verification must run BEFORE `tar -xzf` (verifyIdx=%d, extractIdx=%d)", verifyIdx, extractIdx)
	}
}

// TestInstallScript_AbortsOnChecksumFailure asserts the verification path exits
// non-zero (fails closed) rather than warning-and-continuing.
func TestInstallScript_AbortsOnChecksumFailure(t *testing.T) {
	body := installScriptBody(t)
	// The verify call must be guarded such that failure leads to `exit 1`.
	// Accept either `if ! verify_download_checksum ...; then ... exit 1` or a
	// `verify_download_checksum ... || { ... exit 1; }` shape.
	re := regexp.MustCompile(`(?s)verify_download_checksum.*?exit 1`)
	if !re.MatchString(body) {
		t.Fatalf("a failed verify_download_checksum must `exit 1` (fail closed) — H1")
	}
}

// sourceAndVerify sources install.sh in isolation (without running main) and
// invokes verify_download_checksum with the given args, returning its exit code.
func sourceAndVerify(t *testing.T, file, asset, checksums string) int {
	t.Helper()
	script := installScriptPath(t)
	// AGENT_DECK_INSTALL_SH_SOURCE_ONLY suppresses the `main "$@"` invocation so
	// the function definitions load without performing an install.
	prog := `set +e
export AGENT_DECK_INSTALL_SH_SOURCE_ONLY=1
source "$1"
verify_download_checksum "$2" "$3" "$4"
echo "RC=$?"`
	cmd := exec.Command("bash", "-c", prog, "bash", script, file, asset, checksums)
	out, _ := cmd.CombinedOutput()
	m := regexp.MustCompile(`RC=(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse verifier exit code from output:\n%s", out)
	}
	switch m[1] {
	case "0":
		return 0
	default:
		// Non-zero; map to a single sentinel so callers assert "failed".
		if m[1] == "1" {
			return 1
		}
		// Preserve specific codes (2=missing entry, 3=no tool) for assertions.
		var code int
		for _, c := range m[1] {
			code = code*10 + int(c-'0')
		}
		return code
	}
}

func TestVerifyDownloadChecksum_Functional(t *testing.T) {
	dir := t.TempDir()
	asset := "agent-deck_1.2.3_linux_amd64.tar.gz"
	file := filepath.Join(dir, asset)
	content := []byte("pretend-this-is-a-release-tarball")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	t.Run("matching checksum verifies", func(t *testing.T) {
		checksums := good + "  " + asset + "\n"
		if rc := sourceAndVerify(t, file, asset, checksums); rc != 0 {
			t.Fatalf("expected exit 0 for matching checksum, got %d", rc)
		}
	})

	t.Run("mismatched checksum aborts", func(t *testing.T) {
		bad := strings.Repeat("0", 64)
		checksums := bad + "  " + asset + "\n"
		if rc := sourceAndVerify(t, file, asset, checksums); rc == 0 {
			t.Fatalf("expected non-zero exit for mismatched checksum, got 0")
		}
	})

	t.Run("missing entry aborts", func(t *testing.T) {
		checksums := good + "  some-other-asset.tar.gz\n"
		if rc := sourceAndVerify(t, file, asset, checksums); rc == 0 {
			t.Fatalf("expected non-zero exit when asset absent from checksums.txt, got 0")
		}
	})
}
