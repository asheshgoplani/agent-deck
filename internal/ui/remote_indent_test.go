// Remote rows must align with local root groups.
//
// #1553 nested remote sessions under their group paths but rendered every remote
// row (host header, sub-group, session) with a dedicated 2-col selection-arrow
// column (selPrefix) that local group headers don't reserve. Since one indent
// level is 2 cols, that column shifted the whole remote subtree — including the
// Level-0 host header — one level to the right, so the host header rendered at
// the visual position of Level 1 instead of flush with local root groups.

package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// leadDisplayWidth returns the display width of the text before the first
// occurrence of marker (the indent preceding a row's first glyph).
func leadDisplayWidth(line, marker string) int {
	idx := strings.Index(line, marker)
	if idx < 0 {
		return -1
	}
	return lipgloss.Width(line[:idx])
}

func TestRemoteRows_AlignWithRootGroups_NoExtraIndent(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {{ID: "r1", Title: "worker-1", Status: "running", Group: "work"}},
	}
	sessions := h.remoteSessions["dev"]

	render := func(item session.Item, selected bool) string {
		var b strings.Builder
		switch item.Type {
		case session.ItemTypeRemoteGroup:
			h.renderRemoteGroupItem(&b, item, selected)
		case session.ItemTypeRemoteSession:
			h.renderRemoteSessionItem(&b, item, selected)
		}
		return strings.TrimRight(b.String(), "\n")
	}

	// Level-0 host header sits flush with local root groups: its content starts
	// right after the leftGutterWidth gutter, with no extra arrow column.
	header := render(session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev", Level: 0}, false)
	if w := leadDisplayWidth(header, "▾"); w != leftGutterWidth {
		t.Errorf("remote host header indent = %d cols, want %d (flush with local root groups)\n  line: %q", w, leftGutterWidth, header)
	}

	// Sub-group (Level 1) nests exactly one level under the host header.
	sub := render(session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev/work", Level: 1}, false)
	if w := leadDisplayWidth(sub, "▾"); w != leftGutterWidth+2 {
		t.Errorf("remote sub-group indent = %d cols, want %d (one level under host header)\n  line: %q", w, leftGutterWidth+2, sub)
	}

	// Session (Level 2) nests exactly one level under its sub-group.
	sessLine := render(session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "dev", RemoteSession: &sessions[0], Path: "remotes/dev/work", Level: 2}, false)
	if w := leadDisplayWidth(sessLine, "├"); w != leftGutterWidth+4 {
		t.Errorf("remote session indent = %d cols, want %d (one level under sub-group)\n  line: %q", w, leftGutterWidth+4, sessLine)
	}

	// Selecting a row must not shift it: the arrow replaces the gutter blanks,
	// keeping the same display width.
	headerSel := render(session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev", Level: 0}, true)
	if w := leadDisplayWidth(headerSel, "▾"); w != leftGutterWidth {
		t.Errorf("selected remote host header indent = %d cols, want %d (selection must not shift the row)\n  line: %q", w, leftGutterWidth, headerSel)
	}
}
