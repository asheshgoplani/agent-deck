//go:build eval_smoke

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestEval_EmbeddedSessionKeepsAgentSidebarVisible(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.insertKeySink = nil
	home.insertNamedKeySink = nil
	home.sessionExists = func(*session.Instance) bool { return true }
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return &fakeInsertKeySender{}, nil
	}
	home.previewCache[inst.ID] = "agent is working\n> "
	home.previewCacheTime[inst.ID] = time.Now()

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	frame := ansi.Strip(home.View())
	for _, visible := range []string{"AGENTS  GROUPED", "focused-sessi", "agent is working", "Ctrl+Q detach", "╭", "╯"} {
		if !strings.Contains(frame, visible) {
			t.Fatalf("embedded frame missing %q; attaching must retain both the sidebar and terminal pane.\nFrame:\n%s", visible, frame)
		}
	}

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	home = model.(*Home)
	detachedFrame := ansi.Strip(home.View())
	if !strings.Contains(detachedFrame, "AGENTS  GROUPED") || !strings.Contains(detachedFrame, "focused-sessi") {
		t.Fatalf("detaching should return focus without replacing the dashboard.\nFrame:\n%s", detachedFrame)
	}
	if strings.Contains(detachedFrame, "Ctrl+Q detach") {
		t.Fatalf("detached frame still advertises embedded-session controls.\nFrame:\n%s", detachedFrame)
	}

	t.Run("RemoteSession", func(t *testing.T) {
		t.Skip("RemoteSession: the embedded attach for a remote row is the same PTY path with an ssh command from " +
			"buildRemoteAttachRequest, and its sidebar row is the cached-preview branch; both need a reachable SSH " +
			"host, which the evaluator sandbox does not provide. Row activation for remote items is covered by " +
			"TestEmbeddedDoubleClickActivatesRemoteRow and the Enter handler tests.")
	})
}
