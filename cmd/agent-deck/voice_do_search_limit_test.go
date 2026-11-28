package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 3176 rework R8: recall indexes every conversation on the machine, so the
// voice desk asks it for more hits than it speaks; the owner filter then
// keeps his and the desk cuts to five. Asking for only five lets other
// people's hits crowd his out.
func TestVoiceDoSearchAsksMoreThanItSpeaks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	log := filepath.Join(home, "argv.log")
	self := filepath.Join(home, "fake-agent-deck")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\necho '{\"success\":true,\"result\":{\"hits\":[]}}'\n"
	if err := os.WriteFile(self, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &cliVoiceDeck{self: self, profile: "work"}
	if _, err := d.Search(context.Background(), "invoice"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	argv := string(b)
	if !strings.Contains(argv, "--limit 20 ") {
		t.Errorf("recall search argv %q: want --limit 20", argv)
	}
	if strings.Contains(argv, "--limit 5 ") {
		t.Errorf("recall is asked for only 5 hits before the owner filter: %q", argv)
	}
}
