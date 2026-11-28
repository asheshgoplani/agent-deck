package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// "switch to X", "go to X", "X pe jao" from the phone, glasses or typed
// text are the desk's find forms. The CLI's quick not-mine check must not
// drop them before the desk sees them. A broken config.toml makes the desk
// answer "I cannot read the voice settings" (handled) before any listing,
// so this runs nothing else.
func TestVoiceDoSwitchWordsReachTheDesk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agent-deck", "config.toml"), []byte("[voice\nbroken = "), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"phone", "glasses", "text"} {
		for _, s := range []string{"switch to voice test", "go to voice test"} {
			_, out, errs := runVoiceT(t, "do", "--json", "--from", from, "--", s)
			var m map[string]any
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("%q: %q %q", s, out, errs)
			}
			if m["handled"] != true {
				t.Errorf("%q --from %s: handled=%v; the desk's switch words never reach it", s, from, m["handled"])
			}
		}
	}
	// from the Mac the daemon switches itself: still not the desk's
	_, out, _ := runVoiceT(t, "do", "--json", "--from", "mac", "--", "switch to voice test")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["handled"] != false {
		t.Errorf("--from mac: %q", out)
	}
}
