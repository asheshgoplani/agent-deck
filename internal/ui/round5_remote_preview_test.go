package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestUnreachableRemoteReasonLeadsPreviewAtEveryWidth(t *testing.T) {
	withTempAgentDeckHome(t, "")
	h := NewHome()
	h.remotePolls = map[string]session.RemotePollState{
		"lab": {LastPollStatus: "failed", LastPollError: "host down"},
	}
	item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "lab"}
	for _, width := range []int{80, 120, 200} {
		view := stripAnsi(h.renderRemotePreview(item, width/2, 24))
		reason := strings.Index(view, "Unreachable: host down")
		counts := strings.Index(view, "Sessions")
		if reason < 0 || counts < 0 || reason > counts {
			t.Errorf("%d columns: reason must precede counts:\n%s", width, view)
		}
	}
}
