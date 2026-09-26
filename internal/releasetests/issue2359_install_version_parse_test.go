// issue2359_install_version_parse_test.go pins install.sh's "latest version"
// lookup against a fixture of the REAL GitHub releases API response
// (testdata/issue2359_release_latest.json, fetched live via
// `gh api repos/asheshgoplani/agent-deck/releases/latest`).
//
// The API returns the release body as a single line (no pretty-printing).
// Pre-fix, install.sh parsed it with
// `grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/'`: the grep matches the
// WHOLE document (one line), and the greedy `.*` in the sed capture
// backtracks to the LAST quoted string on that line — which is "eyes", the
// last key in the trailing `"reactions":{...,"eyes":0}` object, not the
// release tag. Every default (`--version` unspecified) install then 404'd
// trying to download release "eyes".
//
// TestParseLatestTag_RealAPIFixture sources install.sh's `parse_latest_tag`
// function in isolation and runs it against the fixture, so a regression to
// the "last quoted string on the line" pattern fails here instead of in the
// wild. TestCountReleaseAssets_RealAPIFixture pins the companion
// ASSET_COUNT bug (grep -c counts matching *lines*, not occurrences, so it
// always reports 0 or 1 on this single-line body regardless of the real
// asset count).
package releasetests

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runInstallShFunc sources install.sh (functions only, via
// AGENT_DECK_INSTALL_SH_SOURCE_ONLY) and pipes fixture into the named
// top-level function, returning its trimmed stdout.
func runInstallShFunc(t *testing.T, fn, fixtureRelPath string) string {
	t.Helper()
	script := installScriptPath(t)
	fixture := filepath.Join(repoRoot(t), fixtureRelPath)

	prog := `set -e
export AGENT_DECK_INSTALL_SH_SOURCE_ONLY=1
source "$1"
"$2" < "$3"`
	cmd := exec.Command("bash", "-c", prog, "bash", script, fn, fixture)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running %s against fixture failed: %v\noutput:\n%s", fn, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestParseLatestTag_RealAPIFixture(t *testing.T) {
	got := runInstallShFunc(t, "parse_latest_tag", "internal/releasetests/testdata/issue2359_release_latest.json")

	if got == "eyes" {
		t.Fatalf("parse_latest_tag returned %q — regressed to #2359 (last quoted string on the line, i.e. the reactions object's \"eyes\" key)", got)
	}
	if got != "v1.16.16" {
		t.Fatalf("parse_latest_tag = %q, want %q (tag_name of the fixture release)", got, "v1.16.16")
	}
}

func TestCountReleaseAssets_RealAPIFixture(t *testing.T) {
	got := runInstallShFunc(t, "count_release_assets", "internal/releasetests/testdata/issue2359_release_latest.json")

	// The fixture's release has 5 assets (4 platform tarballs + checksums.txt).
	// grep -c on the single-line body would report "1" (one matching line),
	// not the real count.
	if got != "5" {
		t.Fatalf("count_release_assets = %q, want %q (grep -c undercounts occurrences on single-line JSON)", got, "5")
	}
}
