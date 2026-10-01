package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// [display] hide_default_tool_badge drops the row's tool badge when the
// session runs default_tool, on every row whatever its status or archive
// state. Any other tool keeps its badge so the outlier stands out.
func TestRenderSessionItem_HidesDefaultToolBadge(t *testing.T) {
	row := func(h *Home, inst *session.Instance) string {
		var b strings.Builder
		h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, nil, 240)
		return ansi.Strip(b.String())
	}
	h := NewHome()
	h.width, h.height = 240, 40
	h.hiddenToolBadge = "claude"

	for _, inst := range []*session.Instance{
		{ID: "a", Title: "running-row", Tool: "claude", Status: session.StatusRunning},
		{ID: "b", Title: "stopped-row", Tool: "claude", Status: session.StatusStopped},
		{ID: "c", Title: "archived-row", Tool: "claude", Status: session.StatusError, ArchivedAt: time.Now()},
	} {
		if got := row(h, inst); strings.Contains(got, "claude") {
			t.Errorf("%s: default-tool badge should be hidden, row = %q", inst.Title, got)
		}
	}
	outlier := &session.Instance{ID: "d", Title: "outlier-row", Tool: "codex", Status: session.StatusStopped}
	if got := row(h, outlier); !strings.Contains(got, "codex") {
		t.Errorf("non-default tool must keep its badge, row = %q", got)
	}

	h.hiddenToolBadge = ""
	if got := row(h, &session.Instance{ID: "e", Title: "off-row", Tool: "claude", Status: session.StatusIdle}); !strings.Contains(got, "claude") {
		t.Errorf("option off: badge must render, row = %q", got)
	}
}
