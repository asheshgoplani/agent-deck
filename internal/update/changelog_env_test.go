package update

import (
	"os"
	"path/filepath"
	"testing"
)

// AGENTDECK_CHANGELOG_URL points every changelog read (the pre-install
// list in `agent-deck update` too) at a local file or another URL.
func TestFetchChangelogHonorsChangelogEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CHANGELOG.md")
	const want = "# Changelog\n\n## [9.9.9] - 2026-11-05\n\n### Fixes\n\n- Local (#1).\n"
	if err := os.WriteFile(p, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ChangelogEnv, p)
	got, err := FetchChangelog()
	if err != nil || got != want {
		t.Fatalf("FetchChangelog = %q, %v; want the file named by %s", got, err, ChangelogEnv)
	}
}
