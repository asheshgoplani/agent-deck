package send

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2536: the composer-draft guard needs a Codex reader. Codex draws its
// composer as the last column-0 "›" cell, followed by an indented footer, and
// shows a dim placeholder while the input is empty.

func issue2536Frame(composer ...string) string {
	rows := append(lines(
		"› run the lint step",
		"",
		"• Ran make lint",
		"  └ ok",
		"",
		"─ Worked for 12s ─────────────────────────────────────",
		"",
	), composer...)
	return strings.Join(rows, "\n") + "\n"
}

func TestIssue2536_CodexComposerDraft(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantDraft   string
		wantVisible bool
	}{
		{"default placeholder dim", issue2536Frame("\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m", "", codexFooter), "", true},
		{"default placeholder without ANSI", issue2536Frame("› Ask Codex to do anything", "", codexFooter), "", true},
		{"rotating placeholder dim", issue2536Frame("› \x1b[2mWrite tests for @filename\x1b[22m", "", codexFooter), "", true},
		{"placeholder on composer background", issue2536Frame("\x1b[48;2;40;40;40m› \x1b[2mExplain this codebase\x1b[0m", codexFooter), "", true},
		{"typed draft", issue2536Frame("› halfway through a refactor plan", "", codexFooter), "halfway through a refactor plan", true},
		{"typed draft with ANSI bold glyph", issue2536Frame("\x1b[1m›\x1b[0m status of lane b", codexFooter), "status of lane b", true},
		{"wrapped draft", issue2536Frame("› first visual row of a long", "  operator draft that wraps", "", codexFooter), "first visual row of a long operator draft that wraps", true},
		{"multi-line draft with blank line", issue2536Frame("› line one", "", "  line three", "", codexFooter), "line one line three", true},
		{"draft starting with newline", issue2536Frame("› ", "  second line typed", codexFooter), "second line typed", true},
		{"empty input row, footer only", issue2536Frame("› ", "", codexFooter), "", true},
		{"no Codex composer", "plain shell output\n$ \n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, visible := CodexComposerDraft(tc.raw, tmux.StripANSI)
			if draft != tc.wantDraft || visible != tc.wantVisible {
				t.Fatalf("CodexComposerDraft = (%q, %v), want (%q, %v)", draft, visible, tc.wantDraft, tc.wantVisible)
			}
		})
	}
}

// Every captured Codex frame in the status corpus ends at an idle composer.
// None of them may read as an operator draft, or every send to an idle Codex
// lane would be held and refused.
func TestIssue2536_CodexCorpusIdleComposersHaveNoDraft(t *testing.T) {
	dir := filepath.Join("..", "tmux", "testdata", "status_corpus")
	labels, err := os.Open(filepath.Join(dir, "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer labels.Close()
	scanner := bufio.NewScanner(labels)
	checked := 0
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 4)
		if len(fields) < 3 || fields[1] != "codex" {
			continue
		}
		frame, err := os.ReadFile(filepath.Join(dir, fields[0]+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		draft, visible := CodexComposerDraft(string(frame), tmux.StripANSI)
		if !visible {
			continue
		}
		checked++
		if draft != "" {
			t.Errorf("%s: idle Codex composer read as draft %q", fields[0], draft)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 15 {
		t.Fatalf("only %d Codex frames with a visible composer", checked)
	}
}

type issue2536Pane struct {
	frames []string
	n      int
}

func (p *issue2536Pane) CapturePaneFresh() (string, error) {
	f := p.frames[min(p.n, len(p.frames)-1)]
	p.n++
	return f, nil
}

func TestIssue2536_GuardUsesCodexReader(t *testing.T) {
	draft := issue2536Frame("› operator draft", "", codexFooter)
	empty := issue2536Frame("› \x1b[2mAsk Codex to do anything\x1b[0m", "", codexFooter)
	opts := ComposerGuardOptions{HoldWait: 20 * time.Millisecond, PollInterval: time.Millisecond, Strip: tmux.StripANSI, Draft: CodexComposerDraft}

	if res := GuardComposerDraft(&issue2536Pane{frames: []string{draft}}, opts); !res.Refused {
		t.Fatalf("Codex draft must be refused, got %+v", res)
	}
	if res := GuardComposerDraft(&issue2536Pane{frames: []string{draft, empty}}, opts); res.Refused {
		t.Fatalf("Codex draft that clears during the hold must not be refused, got %+v", res)
	}
	if res := GuardComposerDraft(&issue2536Pane{frames: []string{empty}}, opts); res.Refused || !res.ComposerPasteMarkerFree {
		t.Fatalf("empty Codex composer must pass immediately, got %+v", res)
	}
}
