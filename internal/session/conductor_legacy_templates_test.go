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
