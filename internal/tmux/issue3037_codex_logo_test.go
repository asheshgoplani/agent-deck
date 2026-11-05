package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// Codex draws its home-screen logo in braille cells; this row begins with a
// cell that is also a frame of the dots spinner (#3037).
const issue3037LogoRow = "⠙⠛⠛⠛⠻⠛⢿⣿⣿⣿⣿⣷⣾⣿⣿⣿⡿⠏"

const issue3037Composer = "\n› Ask Codex to do anything\n  gpt-5.6 · ~/project"

func TestIssue3037CodexLogo(t *testing.T) {
	home, err := os.ReadFile(filepath.Join(statusPaneCorpusDir, "codex", "issue3037-home-screen-braille-logo.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		busy          bool
	}{
		{"home screen", string(home), false},
		{"logo row above composer", issue3037LogoRow + issue3037Composer, false},
		{"logo row with a gap after the frame", "⠋ ⣀⣴⣾⣿⣿⡿⠟⠋ ⣀⡀ ⠈⠙⢿⣿⣿⣧" + issue3037Composer, false},
		{"live status row under the logo", issue3037LogoRow + "\n\n• Working (3s • esc to interrupt)" + issue3037Composer, true},
		{"legacy spinner row under the logo", issue3037LogoRow + "\n⠋ Working (esc to interrupt)" + issue3037Composer, true},
		{"spinner frame with label", "⠋ Thinking" + issue3037Composer, true},
		{"spinner frame alone", "⠹" + issue3037Composer, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession("issue3037", t.TempDir())
			s.Command = "codex"
			if got := s.hasBusyIndicator(tc.content); got != tc.busy {
				t.Fatalf("busy=%v, want %v", got, tc.busy)
			}
		})
	}
}

// The spinner scan is shared by every tool, so the rule is pinned on it
// directly: a frame followed by more braille is a cell of a drawing.
func TestIssue3037BrailleArtIsNotASpinner(t *testing.T) {
	spinners := defaultSpinnerChars()
	for _, tc := range []struct {
		name, line, wantChar string
	}{
		{"logo row", issue3037LogoRow, ""},
		{"logo row with gaps", "⠋ ⣀⣴⣾⣿⣿⡿⠟⠋ ⣀⡀ ⠈⠙⢿⣿⣿⣧", ""},
		{"two frames", "⠋⠙", ""},
		{"frame with label", "⠋ Working…", "⠋"},
		{"frame alone", "⠹", "⠹"},
		{"frame then ellipsis", "⠼ …", "⠼"},
		{"pi banner", "── ⠴ Working ──", "⠴"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			char, _, found := findSpinnerInContent("output\n"+tc.line+"\n❯", spinners)
			if found != (tc.wantChar != "") || char != tc.wantChar {
				t.Fatalf("found=%v char=%q, want %q", found, char, tc.wantChar)
			}
		})
	}
}
