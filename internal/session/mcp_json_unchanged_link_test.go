package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeJSONFileAtomic skips the write when the file already holds the same
// bytes. That shortcut read the file before the containment check, so a
// .mcp.json linked out of the project whose target already matched returned
// success instead of the refusal. The check must come first.
func TestWriteJSONFileAtomic_RefusesOutsideLinkEvenWhenUnchanged(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"mcpServers\": {}}\n")
	outside := filepath.Join(tmp, "victim.json")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(proj, ".mcp.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := writeJSONFileAtomic(proj, link, data, 0o644); err == nil {
		t.Fatal("expected the containment refusal for an unchanged file behind an outside link")
	}
}
