package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRowTruthFramesGolden(t *testing.T) {
	for _, terminalWidth := range []int{80, 120, 200} {
		t.Run(fmt.Sprint(terminalWidth), func(t *testing.T) {
			h := NewHome()
			h.width, h.height = terminalWidth, 40
			width := h.sessionsPaneWidth()
			var b strings.Builder
			group := &session.Group{Name: "alpha", Path: "alpha", Expanded: true}
			h.renderGroupItem(&b, session.Item{Type: session.ItemTypeGroup, Group: group, RootGroupNum: 1}, true, 0, map[string]groupRenderStats{"alpha": {sessionCount: 3}}, width)
			for _, tc := range []struct {
				id, title, pane string
				auto, fork      bool
			}{
				{"normal", "a genuinely long session title", "", false, false},
				{"auto", "thorny-sky", "Review the deployment", true, false},
				{"fork", "a useful fork session title", "", false, true},
			} {
				inst := &session.Instance{ID: tc.id, Title: tc.title, Tool: "claude", Status: session.StatusIdle, AutoName: tc.auto}
				if tc.fork {
					inst.WorktreePath, inst.WorktreeBranch = "/tmp/fork", "fork/claude-many-steps"
				}
				state := sessionRenderState{status: session.StatusIdle, tool: "claude", title: tc.title, paneTitle: tc.pane, autoName: tc.auto}
				h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true}, true, map[string]sessionRenderState{tc.id: state}, width)
			}
			got := fmt.Sprintf("terminal_width=%d list_width=%d\n%s", terminalWidth, width, stripAnsi(b.String()))
			path := filepath.Join("testdata", "row_truth", fmt.Sprintf("%d.txt", terminalWidth))
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
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("%s differs\nwant:\n%s\ngot:\n%s", path, want, got)
			}
		})
	}
}
