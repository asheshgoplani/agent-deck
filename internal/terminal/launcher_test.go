package terminal

import (
	"strings"
	"testing"
)

func TestBuildAttachCommand_NameOnly(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{Name: "myproj"})
	want := "tmux attach -t 'myproj'"
	if got != want {
		t.Fatalf("BuildAttachCommand mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildAttachCommand_WithSocket(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	want := "tmux -L 'agentdeck' attach -t 'myproj'"
	if got != want {
		t.Fatalf("BuildAttachCommand mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildAttachCommand_EmptyNameReturnsEmpty(t *testing.T) {
	if got := BuildAttachCommand(AttachRequest{}); got != "" {
		t.Fatalf("expected empty string for empty name, got %q", got)
	}
	if got := BuildAttachCommand(AttachRequest{Name: "   "}); got != "" {
		t.Fatalf("expected empty string for whitespace name, got %q", got)
	}
}

func TestBuildAttachCommand_QuotingProtectsSingleQuotes(t *testing.T) {
	// Defensive: tmux names are sanitized upstream, but if a single quote
	// ever leaked through, the resulting shell command must still be safe.
	got := BuildAttachCommand(AttachRequest{Name: "weird'name"})
	if !strings.HasPrefix(got, "tmux attach -t '") {
		t.Fatalf("prefix wrong: %q", got)
	}
	if !strings.Contains(got, `'\''`) {
		t.Fatalf("expected escaped single quote in %q", got)
	}
}

func TestShellQuote_NoSpecials(t *testing.T) {
	if got := shellQuote("plain"); got != "'plain'" {
		t.Fatalf("got %q", got)
	}
}
