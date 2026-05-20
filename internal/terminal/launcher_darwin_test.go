//go:build darwin

package terminal

import (
	"strings"
	"testing"
)

// On darwin, buildITerm2AppleScript should embed the tmux attach command
// inside an AppleScript that targets iTerm2's default profile. We verify the
// script shape without actually invoking osascript.
func TestBuildITerm2AppleScript_EmbedsAttachCommand(t *testing.T) {
	cmd := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	script := buildITerm2AppleScript(cmd)

	for _, want := range []string{
		`tell application "iTerm2"`,
		`create window with default profile`,
		`tmux -L 'agentdeck' attach -t 'myproj'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("AppleScript missing %q\nfull script:\n%s", want, script)
		}
	}
}

func TestBuildITerm2AppleScript_EscapesDoubleQuotes(t *testing.T) {
	// Defensive: if a tmux name ever contained " or \, the AppleScript
	// must escape it so the surrounding double-quoted literal stays valid.
	script := buildITerm2AppleScript(`echo "hi" \ bye`)
	if strings.Contains(script, `"hi"`) {
		t.Fatalf("double quotes inside command leaked into AppleScript literal:\n%s", script)
	}
	if !strings.Contains(script, `\"hi\"`) {
		t.Fatalf("expected escaped quotes \\\"hi\\\" in script:\n%s", script)
	}
}
