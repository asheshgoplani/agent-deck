package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestSessionRowWidthBudget_Golden pins the session-list row width budget
// (#2201) at 80 and 60 terminal columns, using the same pane-width math the
// real TUI uses (sessionsPaneWidth) so the frames match what a user sees.
// Before the fix, a long title collapsed to a bare "…" at these widths while
// the account badge stayed fully visible; now the title keeps at least
// minSessionTitleWidth columns and the badge shrinks or drops instead.
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestSessionRowWidthBudget_Golden
func TestSessionRowWidthBudget_Golden(t *testing.T) {
	steps := []struct {
		name          string
		terminalWidth int
		account       string
		fork          bool
	}{
		{"01-term80-inherited", 80, "", false},
		{"02-term60-inherited", 60, "", false},
		{"03-term80-named", 80, "personal", false},
		{"04-term60-named", 60, "personal", false},
		{"05-term80-fork", 80, "", true},
		{"06-term120-fork", 120, "", true},
		{"07-term200-fork", 200, "", true},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			h := NewHome()
			h.width, h.height = step.terminalWidth, 40
			listWidth := h.sessionsPaneWidth()
			inst := &session.Instance{
				ID:      "width",
				Title:   "a genuinely long session title that would elide the account suffix first",
				Tool:    "claude",
				Status:  session.StatusIdle,
				Account: step.account,
			}
			if step.fork {
				inst.WorktreePath = "/tmp/fork"
				inst.WorktreeBranch = "fork/claude-many-steps"
			}
			state := sessionRenderState{
				status:         session.StatusIdle,
				tool:           "claude",
				title:          inst.Title,
				account:        step.account,
				accountDisplay: newAccountPresentation(step.account, true),
			}
			var b strings.Builder
			h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, map[string]sessionRenderState{inst.ID: state}, listWidth)
			got := fmt.Sprintf("terminal_width=%d list_width=%d\n%s", step.terminalWidth, listWidth, strings.TrimSuffix(stripAnsi(b.String()), "\n")+"\n")
			path := filepath.Join("testdata", "session_row_width_budget", step.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
			}
			if string(want) != got {
				t.Fatalf("golden %s differs from the rendered row.\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		})
	}
}
