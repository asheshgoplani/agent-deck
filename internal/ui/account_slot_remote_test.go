package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// RemoteSessionInfo does not transport Account. A local snapshot with the same
// ID must not invent a remote slot or label unknown remote metadata inherited.
func TestStoredAccountRemoteRowDoesNotInventSlot(t *testing.T) {
	for _, width := range []int{40, 120} {
		for _, selected := range []bool{false, true} {
			h := NewHome()
			h.width, h.height = width, 40
			h.refreshSessionRenderSnapshot([]*session.Instance{{ID: "same-id", Title: "local", Tool: "shell", Account: "private-local-slot"}})
			remote := session.RemoteSessionInfo{ID: "same-id", Title: "remote日本", Tool: "shell", Status: "idle", RemoteName: "dev"}
			var b strings.Builder
			h.renderRemoteSessionItem(&b, session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "dev"}, selected)
			row := strings.TrimSuffix(b.String(), "\n")
			require.Contains(t, row, "remote日本")
			require.NotContains(t, row, "[account:")
			require.NotContains(t, row, "private-local-slot")
			require.NotContains(t, row, "inherited")
			require.True(t, utf8.ValidString(row))
			require.LessOrEqual(t, cellWidth(row), width)
			require.Equal(t, 1, strings.Count(b.String(), "\n"))
		}
	}
}
