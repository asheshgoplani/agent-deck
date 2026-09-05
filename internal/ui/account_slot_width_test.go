package ui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func TestStoredAccountWidthMatrix(t *testing.T) {
	for _, slot := range []string{"", "personal", strings.Repeat("日本e\u03011️⃣🚀", 20), "\x1b]0;bad\a\r\n\u202e"} {
		for _, width := range []int{24, 40, 72, 120} {
			for _, auto := range []bool{false, true} {
				t.Run(fmt.Sprintf("%q/%d/auto=%v", slot, width, auto), func(t *testing.T) {
					h := NewHome()
					h.width, h.height = width, 40
					inst := &session.Instance{ID: "width", Title: strings.Repeat("Title日本e\u0301", 8), Tool: "shell", Status: session.StatusIdle, Account: slot}
					state := sessionRenderState{status: session.StatusIdle, tool: "shell", title: inst.Title, account: slot, autoName: auto, paneTitle: "pane subtitle", autoNameDesc: "description"}
					for _, selected := range []bool{false, true} {
						var b strings.Builder
						h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, selected, map[string]sessionRenderState{inst.ID: state}, width)
						line := strings.TrimSuffix(b.String(), "\n")
						require.True(t, utf8.ValidString(line))
						require.LessOrEqual(t, cellWidth(line), width)
						require.NotContains(t, line, "\x1b]0;bad")
						require.NotContains(t, line, "\a")
						require.NotContains(t, line, "\r")
						require.NotContains(t, line, "\n")
						if width >= 40 {
							require.Contains(t, line, "[account:")
						}
					}
				})
			}
		}
	}
}

func TestStoredAccountSnapshotIsAuthoritative(t *testing.T) {
	h := NewHome()
	inst := &session.Instance{ID: "snapshot", Title: "title", Tool: "shell", Status: session.StatusIdle, Account: "first"}
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	inst.Account = "second" // Sequential controlled mutation; no concurrent writer.
	require.Equal(t, "first", h.getSessionRenderState(inst).account)
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	require.Equal(t, "second", h.getSessionRenderState(inst).account)
}
