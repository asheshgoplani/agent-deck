package update

// Regression tests for #1206 (security): `agent-deck remote update` deployed
// release binaries to remotes with NO integrity check. These tests pin the
// fail-closed SHA-256 verification gate: a matching checksum proceeds, a
// mismatch or a missing checksum aborts WITHOUT returning a binary to deploy.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeTarGz builds a goreleaser-style .tar.gz containing an `agent-deck` binary
// with the given bytes, so DownloadVerifiedBinary can extract it.
func makeTarGz(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "agent-deck",
		Mode:     0o755,
		Size:     int64(len(binary)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestParseChecksums_GoreleaserFormat(t *testing.T) {
	// goreleaser checksums.txt: "<hex>  <filename>", optional "*" binary-mode
	// prefix, blank lines tolerated.
	body := "abc123  agent-deck_1.2.3_linux_amd64.tar.gz\n" +
		"\n" +
		"DEF456  *agent-deck_1.2.3_darwin_arm64.tar.gz\n"
	got := ParseChecksums([]byte(body))
	if got["agent-deck_1.2.3_linux_amd64.tar.gz"] != "abc123" {
		t.Fatalf("linux entry = %q, want abc123", got["agent-deck_1.2.3_linux_amd64.tar.gz"])
	}
	// hex normalized to lowercase, "*" stripped.
	if got["agent-deck_1.2.3_darwin_arm64.tar.gz"] != "def456" {
		t.Fatalf("darwin entry = %q, want def456 (lowercased, * stripped)", got["agent-deck_1.2.3_darwin_arm64.tar.gz"])
	}
}

func TestGetChecksumsURL(t *testing.T) {
	rel := &Release{Assets: []Asset{
		{Name: "agent-deck_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: "https://x/bin"},
		{Name: "checksums.txt", BrowserDownloadURL: "https://x/checksums.txt"},
	}}
	if url := GetChecksumsURL(rel); url != "https://x/checksums.txt" {
		t.Fatalf("GetChecksumsURL = %q, want the checksums.txt asset URL", url)
	}
	// Fail closed: no checksums asset → empty.
	none := &Release{Assets: []Asset{{Name: "agent-deck_1.2.3_linux_amd64.tar.gz"}}}
	if url := GetChecksumsURL(none); url != "" {
		t.Fatalf("GetChecksumsURL with no checksums asset = %q, want empty", url)
	}
}

func TestVerifyAssetChecksum_Match(t *testing.T) {
	archive := []byte("the-archive-bytes")
	sums := map[string]string{"asset.tar.gz": sha256hex(archive)}
	if err := VerifyAssetChecksum("asset.tar.gz", archive, sums); err != nil {
		t.Fatalf("matching checksum should pass, got: %v", err)
	}
}

func TestVerifyAssetChecksum_Mismatch(t *testing.T) {
	archive := []byte("the-archive-bytes")
	sums := map[string]string{"asset.tar.gz": sha256hex([]byte("DIFFERENT"))}
	err := VerifyAssetChecksum("asset.tar.gz", archive, sums)
	if err == nil {
		t.Fatal("mismatched checksum MUST return an error (fail closed), got nil")
	}
}

func TestVerifyAssetChecksum_Missing(t *testing.T) {
	archive := []byte("the-archive-bytes")
	// asset absent from the checksums map → must fail closed.
	err := VerifyAssetChecksum("asset.tar.gz", archive, map[string]string{})
	if err == nil {
		t.Fatal("missing checksum entry MUST return an error (fail closed), got nil")
	}
}

// releaseServer spins up an httptest server hosting the archive and a
// checksums.txt, returning a Release whose asset URLs point at it.
func releaseServer(t *testing.T, archive, checksums []byte, includeChecksumsAsset bool) (*Release, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/agent-deck_1.2.3_linux_amd64.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(checksums)
	})
	srv := httptest.NewServer(mux)
	rel := &Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "agent-deck_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: srv.URL + "/agent-deck_1.2.3_linux_amd64.tar.gz"},
		},
	}
	if includeChecksumsAsset {
		rel.Assets = append(rel.Assets, Asset{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"})
	}
	return rel, srv.Close
}

func TestDownloadVerifiedBinary_MatchingChecksumProceeds(t *testing.T) {
	binary := []byte("ELF-agent-deck-binary")
	archive := makeTarGz(t, binary)
	checksums := []byte(sha256hex(archive) + "  agent-deck_1.2.3_linux_amd64.tar.gz\n")
	rel, cleanup := releaseServer(t, archive, checksums, true)
	defer cleanup()

	got, err := DownloadVerifiedBinary(rel, "linux", "amd64")
	if err != nil {
		t.Fatalf("matching checksum should deploy, got error: %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("returned binary = %q, want extracted %q", got, binary)
	}
}

func TestDownloadVerifiedBinary_MismatchedChecksumAborts(t *testing.T) {
	binary := []byte("ELF-agent-deck-binary")
	archive := makeTarGz(t, binary)
	// Published checksum is for a DIFFERENT artifact → tampered/corrupt.
	checksums := []byte(sha256hex([]byte("tampered")) + "  agent-deck_1.2.3_linux_amd64.tar.gz\n")
	rel, cleanup := releaseServer(t, archive, checksums, true)
	defer cleanup()

	got, err := DownloadVerifiedBinary(rel, "linux", "amd64")
	if err == nil {
		t.Fatal("mismatched checksum MUST abort the deploy, got nil error")
	}
	if got != nil {
		t.Fatalf("on mismatch no binary may be returned, got %d bytes", len(got))
	}
	if !strings.Contains(strings.ToLower(err.Error()), "mismatch") {
		t.Fatalf("error should name the SHA-256 mismatch, got: %v", err)
	}
}

func TestDownloadVerifiedBinary_MissingChecksumsAssetAborts(t *testing.T) {
	binary := []byte("ELF-agent-deck-binary")
	archive := makeTarGz(t, binary)
	// Release publishes NO checksums.txt asset → cannot verify → fail closed.
	rel, cleanup := releaseServer(t, archive, nil, false)
	defer cleanup()

	got, err := DownloadVerifiedBinary(rel, "linux", "amd64")
	if err == nil {
		t.Fatal("a release without checksums.txt MUST abort the deploy (fail closed), got nil error")
	}
	if got != nil {
		t.Fatalf("no binary may be returned when checksums are unavailable, got %d bytes", len(got))
	}
}

func TestDownloadVerifiedBinary_AssetNotInChecksumsAborts(t *testing.T) {
	binary := []byte("ELF-agent-deck-binary")
	archive := makeTarGz(t, binary)
	// checksums.txt exists but lists a different filename → our asset is unverified.
	checksums := []byte(sha256hex(archive) + "  agent-deck_1.2.3_windows_amd64.tar.gz\n")
	rel, cleanup := releaseServer(t, archive, checksums, true)
	defer cleanup()

	if _, err := DownloadVerifiedBinary(rel, "linux", "amd64"); err == nil {
		t.Fatal("an asset absent from checksums.txt MUST abort the deploy (fail closed), got nil error")
	}
}

// Ensure the verified path computes the same archive name the download path
// fetches, so a real release's checksums entry will be found.
func TestAssetArchiveName_MatchesAssetURL(t *testing.T) {
	rel := &Release{TagName: "v1.2.3", Assets: []Asset{
		{Name: "agent-deck_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: "https://x/a"},
	}}
	name := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", "1.2.3", "linux", "amd64")
	if GetAssetURLForPlatform(rel, "linux", "amd64") == "" {
		t.Fatalf("asset URL lookup failed for computed name %q", name)
	}
}
