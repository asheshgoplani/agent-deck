//go:build darwin

package terminal

import (
	"strings"
	"testing"
)

// On darwin, buildITerm2AppleScript should embed the tmux attach command
// inside an AppleScript that targets iTerm2's default profile. We verify the
// script shape without actually invoking osascript.
//
// Issue #1100: the script now branches on openAs. "window" preserves the
// pre-#1100 "create window with default profile" behavior; everything
// else defaults to a new tab in the frontmost window.
func TestBuildITerm2AppleScript_WindowModeCreatesWindow(t *testing.T) {
	cmd := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	script := buildITerm2AppleScript(cmd, "window")

	for _, want := range []string{
		`tell application "iTerm2"`,
		`create window with default profile`,
		`tmux -L 'agentdeck' attach -t 'myproj'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("AppleScript missing %q\nfull script:\n%s", want, script)
		}
	}
	if strings.Contains(script, "create tab with default profile") {
		t.Errorf("window mode should NOT create a tab:\n%s", script)
	}
}

// TestBuildITerm2AppleScript_TabModeIsDefault pins issue #1100 fix (b):
// Shift+Enter must open a TAB by default (iTerm's natural UX), not a
// fresh detached window like #1098 originally shipped.
func TestBuildITerm2AppleScript_TabModeIsDefault(t *testing.T) {
	cmd := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	// Empty openAs == default.
	script := buildITerm2AppleScript(cmd, "")

	for _, want := range []string{
		`tell application "iTerm2"`,
		`create tab with default profile`,
		`tmux -L 'agentdeck' attach -t 'myproj'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("default-mode AppleScript missing %q\nfull script:\n%s", want, script)
		}
	}
	// The tab variant must still cover the no-windows-open fallback by
	// creating a fresh window — otherwise Shift+Enter from a state with
	// no iTerm windows would no-op.
	if !strings.Contains(script, "create window with default profile") {
		t.Errorf("tab-mode AppleScript missing the (count of windows) = 0 fallback:\n%s", script)
	}
}

// TestBuildITerm2AppleScript_TabModeExplicit verifies the explicit "tab"
// value behaves identically to the default fallback (issue #1100).
func TestBuildITerm2AppleScript_TabModeExplicit(t *testing.T) {
	cmd := BuildAttachCommand(AttachRequest{Name: "myproj"})
	defaultScript := buildITerm2AppleScript(cmd, "")
	tabScript := buildITerm2AppleScript(cmd, "tab")
	if defaultScript != tabScript {
		t.Fatalf("explicit \"tab\" should match default mode\ndefault:\n%s\ntab:\n%s",
			defaultScript, tabScript)
	}
}

// TestBuildITerm2SplitPaneAppleScript_Normal verifies the split-pane script
// contains the expected iTerm2 API call and the attach command. Issue #1470.
func TestBuildITerm2SplitPaneAppleScript_Normal(t *testing.T) {
	cmd := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	script := buildITerm2SplitPaneAppleScript(cmd)

	for _, want := range []string{
		`tell application "iTerm2"`,
		`split vertically with default profile`,
		`tmux -L 'agentdeck' attach -t 'myproj'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("split-pane AppleScript missing %q\nfull script:\n%s", want, script)
		}
	}
	// Must NOT create a new tab or window — that's openInNewWindow's job.
	if strings.Contains(script, "create tab with default profile") {
		t.Errorf("split-pane script must not create a tab:\n%s", script)
	}
	if strings.Contains(script, "create window with default profile") {
		t.Errorf("split-pane script must not create a window:\n%s", script)
	}
}

// TestBuildITerm2SplitPaneAppleScript_Escaping ensures quotes and backslashes
// in the command are escaped so they cannot break the AppleScript literal.
// Issue #1470.
func TestBuildITerm2SplitPaneAppleScript_Escaping(t *testing.T) {
	script := buildITerm2SplitPaneAppleScript(`echo "hello" \ world`)

	if strings.Contains(script, `"hello"`) {
		t.Errorf("unescaped double-quote leaked into split-pane AppleScript literal:\n%s", script)
	}
	if !strings.Contains(script, `\"hello\"`) {
		t.Errorf("expected escaped quotes \\\"hello\\\" in script:\n%s", script)
	}
	if strings.Contains(script, ` \ `) {
		t.Errorf("unescaped backslash leaked into split-pane AppleScript literal:\n%s", script)
	}
	if !strings.Contains(script, `\\ world`) {
		t.Errorf("expected escaped backslash in split-pane AppleScript literal:\n%s", script)
	}
}

func TestBuildITerm2AppleScript_EscapesDoubleQuotes(t *testing.T) {
	// Defensive: if a tmux name ever contained " or \, the AppleScript
	// must escape it so the surrounding double-quoted literal stays valid.
	// Exercise both modes — the tab branch interpolates the command
	// twice (fallback + tab path) and both copies must be escaped.
	for _, openAs := range []string{"", "window"} {
		script := buildITerm2AppleScript(`echo "hi" \ bye`, openAs)
		if strings.Contains(script, `"hi"`) {
			t.Errorf("openAs=%q: double quotes inside command leaked into AppleScript literal:\n%s",
				openAs, script)
		}
		if !strings.Contains(script, `\"hi\"`) {
			t.Errorf("openAs=%q: expected escaped quotes \\\"hi\\\" in script:\n%s",
				openAs, script)
		}
	}
}
