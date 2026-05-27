package update

// Release-asset integrity verification (#1206 security hardening).
//
// `agent-deck remote update` downloads a GitHub release asset and pipes it onto
// every configured remote via `ssh "cat > path"`. HTTPS authenticates the
// transport but not the artifact: a compromised release, a swapped asset, or a
// TLS-stripping position can plant an arbitrary executable on every remote.
// goreleaser publishes a `checksums.txt` (SHA-256, see .goreleaser.yml
// `checksum.name_template`); we verify the downloaded archive against it and
// fail closed — never deploying an unverified artifact.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChecksumsAssetName is the release asset goreleaser publishes containing the
// SHA-256 of every archive (.goreleaser.yml `checksum.name_template`).
const ChecksumsAssetName = "checksums.txt"

// ParseChecksums parses a goreleaser checksums.txt body into a map of
// filename -> lowercase hex SHA-256. Each non-blank line is
// "<hex><space(s)><filename>"; a "*" binary-mode prefix on the filename
// (as emitted by `sha256sum -b`) is tolerated and stripped.
func ParseChecksums(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		name := strings.TrimPrefix(fields[1], "*")
		out[name] = sum
	}
	return out
}

// GetChecksumsURL returns the download URL of the release's checksums.txt asset,
// or "" when the release publishes none (which callers must treat as a hard
// failure — there is nothing to verify against).
func GetChecksumsURL(release *Release) string {
	if release == nil {
		return ""
	}
	for _, a := range release.Assets {
		if a.Name == ChecksumsAssetName {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// VerifyAssetChecksum fails closed: it returns an error unless assetName is
// present in checksums AND the SHA-256 of archive matches it exactly. A missing
// entry or a mismatch is a refusal to deploy, not a warning.
func VerifyAssetChecksum(assetName string, archive []byte, checksums map[string]string) error {
	want, ok := checksums[assetName]
	if !ok || strings.TrimSpace(want) == "" {
		return fmt.Errorf("no published SHA-256 checksum for %q in %s — refusing to deploy an unverified artifact", assetName, ChecksumsAssetName)
	}
	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 mismatch for %q: published %s, downloaded %s — refusing to deploy a tampered or corrupt artifact", assetName, want, got)
	}
	return nil
}

// assetArchiveName returns the goreleaser archive filename for a version+platform.
// It mirrors the name template in .goreleaser.yml and the lookup in
// GetAssetURLForPlatform, so the checksums.txt entry will be found.
func assetArchiveName(release *Release, goos, goarch string) string {
	version := strings.TrimPrefix(release.TagName, "v")
	return fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, goos, goarch)
}

// httpGetBytes downloads url fully into memory under a bounded timeout.
func httpGetBytes(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// DownloadVerifiedBinary downloads the release archive for goos/goarch, verifies
// its SHA-256 against the release's checksums.txt, then extracts and returns the
// agent-deck binary bytes. It is the integrity gate for remote deploys and is
// safe to reuse for local self-update. It fails closed: a missing platform
// asset, a missing checksums.txt asset, an asset absent from checksums.txt, or a
// hash mismatch all abort BEFORE any binary is returned.
func DownloadVerifiedBinary(release *Release, goos, goarch string) ([]byte, error) {
	if release == nil {
		return nil, fmt.Errorf("nil release")
	}
	assetURL := GetAssetURLForPlatform(release, goos, goarch)
	if assetURL == "" {
		return nil, fmt.Errorf("no release binary available for %s/%s", goos, goarch)
	}
	checksumsURL := GetChecksumsURL(release)
	if checksumsURL == "" {
		return nil, fmt.Errorf("release %s publishes no %s — refusing to deploy an unverified artifact", release.TagName, ChecksumsAssetName)
	}

	archive, err := httpGetBytes(assetURL, 120*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to download release archive: %w", err)
	}
	checksumsData, err := httpGetBytes(checksumsURL, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s: %w", ChecksumsAssetName, err)
	}

	assetName := assetArchiveName(release, goos, goarch)
	if err := VerifyAssetChecksum(assetName, archive, ParseChecksums(checksumsData)); err != nil {
		return nil, err
	}

	return extractBinaryFromTarGzBytes(archive)
}
