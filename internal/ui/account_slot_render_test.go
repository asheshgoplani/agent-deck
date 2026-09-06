package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// These controls intentionally use only pre-existing rendering entry points.
// They must reproduce missing stored-slot output on the unmodified baseline.
func TestStoredAccountRenderBaseline(t *testing.T) {
	slots := []string{"personal", "work", "", "inherited", " 日本 e\u0301 🚀 ", "quote\"slash\\", "\x1b]0;injected\a\r\n\u0085\u202e"}
	for _, account := range slots {
		t.Run(strconv.Quote(account), func(t *testing.T) {
			inst := &session.Instance{ID: "account-render", Title: "same-title", Tool: "shell", Status: session.StatusIdle, Account: account}
			label := strconv.Quote(account)
			if account == "" {
				label = "inherited"
			}
			for _, refreshed := range []bool{false, true} {
				name := "fallback"
				if refreshed {
					name = "snapshot"
				}
				t.Run(name, func(t *testing.T) {
					h := NewHome()
					h.width, h.height = 240, 40
					if refreshed {
						h.refreshSessionRenderSnapshot([]*session.Instance{inst})
					}
					for _, selected := range []bool{false, true} {
						var b strings.Builder
						h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, selected, h.getSessionRenderSnapshot(), 240)
						row := b.String()
						require.Contains(t, row, "[account:"+label+"]", "stored slot must be visible on each session row")
						require.Equal(t, 1, strings.Count(row, "\n"), "account controls cannot add logical rows")
						require.NotContains(t, row, "\x1b]0;injected")
						require.NotContains(t, row, "\a")
					}
				})
				t.Run(name+"_card", func(t *testing.T) {
					h := NewHome()
					if refreshed {
						h.refreshSessionRenderSnapshot([]*session.Instance{inst})
					}
					card := h.renderSessionInfoCard(inst, 240, 40)
					require.Contains(t, card, "Account slot:")
					require.Contains(t, card, label)
					require.NotContains(t, card, "\x1b]0;injected")
				})
			}
		})
	}
}

func TestStoredAccountBaselineKeepsTitleAndRecency(t *testing.T) {
	h := NewHome()
	h.width, h.height = 240, 40
	h.showSessionTimestamps = true
	inst := &session.Instance{ID: "account-control", Title: "existing-title", Tool: "shell", Status: session.StatusIdle, Account: "personal"}
	var b strings.Builder
	h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, nil, 240)
	require.Contains(t, b.String(), "existing-title")
	require.Equal(t, 1, strings.Count(b.String(), "\n"))
}
