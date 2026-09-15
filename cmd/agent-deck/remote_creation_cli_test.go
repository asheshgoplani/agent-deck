package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteCreationCLIHelpAndCatalog(t *testing.T) {
	home := t.TempDir()
	for _, command := range []string{"add", "launch"} {
		out, stderr, code := runAgentDeck(t, home, command, "--capabilities", "--json")
		if code != 0 {
			t.Fatalf("%s catalog code %d: %s %s", command, code, out, stderr)
		}
		var catalog session.RemoteCreationCatalog
		if err := json.Unmarshal([]byte(out), &catalog); err != nil {
			t.Fatalf("non-JSON catalog: %v %q", err, out)
		}
		if catalog.Version != 1 || len(catalog.Commands[command]) == 0 {
			t.Fatalf("invalid catalog %+v", catalog)
		}
		out, stderr, code = runAgentDeck(t, home, command, "--help")
		// flag.ExitOnError historically uses 0 for the built-in help path.
		if code != 0 || !strings.Contains(out+stderr, "startup-query") || !strings.Contains(out+stderr, "additional-path") {
			t.Fatalf("%s help code=%d: %s %s", command, code, out, stderr)
		}
	}
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "state.db" {
			t.Errorf("read-only catalog created registry %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
