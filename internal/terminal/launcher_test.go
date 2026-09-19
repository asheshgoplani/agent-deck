package terminal

import (
	"os/exec"
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

// The embedded client asks for tmux's global -u so a dashboard launched under
// a non-UTF-8 locale still renders non-ASCII output; the flag precedes the
// socket and the attach verb exactly as internal/tmux/pty.go orders it.
func TestBuildAttachCommand_ForceUTF8(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck", ForceUTF8: true})
	want := "tmux -u -L 'agentdeck' attach -t 'myproj'"
	if got != want {
		t.Fatalf("BuildAttachCommand mismatch:\n got=%q\nwant=%q", got, want)
	}
	if got := BuildAttachCommand(AttachRequest{Name: "myproj", ForceUTF8: true}); got != "tmux -u attach -t 'myproj'" {
		t.Fatalf("BuildAttachCommand without socket = %q", got)
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

// TestShellQuote_SafeAgainstShellInjection proves the property
// internal/ui/embedded_terminal.go relies on when it wraps BuildAttachCommand's
// output in "sh -c exec <command>" (gosec G204/G702, "command injection via
// taint analysis"): the value shellQuote produces is program-controlled data
// to the shell, not additional shell syntax, so a request field that reached
// this point full of shell metacharacters can only ever come out the other
// side unchanged. Runs a real /bin/sh rather than asserting on the string
// shape, since that's the only thing gosec's taint model — and an actual
// attacker — cares about. Uses printf/echo only, no filesystem or process
// side effects even if the quoting were broken.
func TestShellQuote_SafeAgainstShellInjection(t *testing.T) {
	for _, malicious := range []string{
		`x'; echo INJECTED #`,
		"x`echo INJECTED`",
		"x$(echo INJECTED)",
		"x && echo INJECTED",
		"x | echo INJECTED",
		"x\necho INJECTED",
	} {
		quoted := shellQuote(malicious)
		out, err := exec.Command("sh", "-c", "printf '%s' "+quoted).Output()
		if err != nil {
			t.Fatalf("sh -c failed for %q: %v", malicious, err)
		}
		if got := string(out); got != malicious {
			t.Fatalf("shell metacharacters were not neutralized: input %q, shell produced %q", malicious, got)
		}
	}
}
