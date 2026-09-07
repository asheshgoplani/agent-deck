package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The cold-eye reviewer was handed the binary and one sentence. They never found
// the context inspector, because `agent-deck --help` did not mention it in any
// form — not in the command list, not in the keyboard shortcuts, not in the
// examples. Then they guessed at a name for it, and the guess opened a
// full-screen TUI on a pipe and hung their terminal until they killed it.
//
// The tests below pin both halves of that. They are cheap and they are dull,
// which is the point: an undiscoverable feature fails a user before any of its
// carefully-hedged numbers get the chance to.

// captureHelp runs printHelp with stdout redirected and returns what it wrote.
func captureHelp(t *testing.T) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	printHelp()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func TestHelpMentionsTheContextInspector(t *testing.T) {
	help := captureHelp(t)

	// The command itself, in the block a reader scans for session verbs.
	if !strings.Contains(help, "session context") {
		t.Error("`agent-deck help` never names `session context`: the feature cannot be found by reading the help")
	}
	// The hotkey, in the block that lists what the TUI responds to. Without it
	// the only way to discover C is to press every key on the keyboard.
	shortcuts := help[strings.Index(help, "Keyboard shortcuts (in TUI):"):]
	if !strings.Contains(shortcuts, "  C  ") {
		t.Error("`Keyboard shortcuts (in TUI)` omits C: the TUI entry point is undiscoverable")
	}
	if !strings.Contains(shortcuts, "Inspect context") {
		t.Error("the C shortcut is listed without saying what it does")
	}
}

// TestUnknownCommandsAreSuggestedNotSwallowed pins the words a first typo meets.
//
// Each of these was an actual guess at the name of `session context`. Before the
// fix every one of them fell through the subcommand switch and launched the TUI.
func TestUnknownCommandsAreSuggestedNotSwallowed(t *testing.T) {
	for _, guess := range []string{"context", "ctx", "inspect", "tokens", "usage", "CONTEXT", " ctx "} {
		if got := suggestCommand(guess); got != "agent-deck session context [id]" {
			t.Errorf("suggestCommand(%q) = %q, want the session context command", guess, got)
		}
	}
}

// TestSuggestCommandStaysSilentWhenItHasNothingToSay keeps the guard honest: a
// wrong suggestion is worse than none, so an unrecognised word gets the command
// list and not a guess.
func TestSuggestCommandStaysSilentWhenItHasNothingToSay(t *testing.T) {
	for _, arg := range []string{"", "   ", "wibble", "--json"} {
		if got := suggestCommand(arg); got != "" {
			t.Errorf("suggestCommand(%q) = %q, want no suggestion", arg, got)
		}
	}
}
