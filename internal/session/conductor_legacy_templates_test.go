package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conductor/setup.sh still installs conductor/conductor-claude.md and
// conductor/conductor-hermes.md. Their /sessions row must match the row the
// Go setup writes (conductorSessionsQuickCommandRow), which skips archived
// sessions, with the {PROFILE} placeholder those files use (issue #2986 item 5).
func TestLegacyConductorTemplates_SessionsRowSkipsArchived(t *testing.T) {
	want := strings.TrimSuffix(strings.ReplaceAll(conductorSessionsQuickCommandRow, "<PROFILE>", "{PROFILE}"), "\n")
	for _, name := range []string{"conductor-claude.md", "conductor-hermes.md"} {
		path := filepath.Join("..", "..", "conductor", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var row string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "| `/sessions` |") {
				row = line
				break
			}
		}
		if row == "" {
			t.Fatalf("%s: no /sessions quick-command row", name)
		}
		if row != want {
			t.Errorf("%s: /sessions row\n got: %s\nwant: %s", name, row, want)
		}
	}
}

// The legacy templates must describe the heartbeat reply the way the bridge
// parses it today (tiers from #2469), not the pre-tier "only NEED: is
// forwarded" wording.
func TestLegacyConductorTemplates_HeartbeatReplyDocMatchesTiers(t *testing.T) {
	want := "NEED: api-fix - asking whether to run integration tests against staging or prod\n" + conductorHeartbeatReplyDoc
	for _, name := range []string{"conductor-claude.md", "conductor-hermes.md"} {
		path := filepath.Join("..", "..", "conductor", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s: heartbeat reply format does not match the shared template's tiered wording", name)
		}
		if strings.Contains(string(raw), "if it contains `NEED:` lines, those get") {
			t.Errorf("%s: still says only NEED: lines are forwarded", name)
		}
	}
}
