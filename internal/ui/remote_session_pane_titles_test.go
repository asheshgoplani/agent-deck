package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RemoteSession pane-title parity is intentionally deferred.
//
// The session.RemoteSessionInfo struct (internal/session/ssh.go) carries
// only ID, Title, Path, Group, Tool, Status, CreatedAt, and a locally-set
// RemoteName. It does NOT carry a pane title. The dim pane-title suffix is
// derived from per-session tmux state (sessionRenderState.paneTitle) that
// lives on the remote machine and is never shipped over the wire to the
// local TUI.
//
// The local suffix in renderSessionItem is gated on
// (selected || h.showPaneTitles) && instState.paneTitle != "". The remote
// renderer renderRemoteSessionItem takes no snapshot and has no paneTitle
// source, so enabling [display] show_pane_titles cannot surface a pane
// title on a remote row.
//
// Until the remote protocol is extended to ship pane-title state, pane
// titles stay local-only. This test pins that decision: it confirms the
// suffix does NOT leak into remote rows when the flag is on, so a future
// change that silently adds it here is forced through a conscious
// re-review. It mirrors remote_session_timestamps_test.go.
//
// Tracking note for follow-up: extending the wire format / RemoteSessionInfo
// to carry the tmux pane title would be the minimum needed to make remote
// pane-title parity meaningful.
func TestRemoteSession_PaneTitleIsLocalOnly(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30
	home.showPaneTitles = true // would emit the suffix on local rows

	const sentinelPaneTitle = "Explore messaging support features"

	remote := session.RemoteSessionInfo{
		ID:         "remote-test",
		Title:      "remote-session",
		Status:     "running",
		Tool:       "claude",
		RemoteName: "myserver",
	}
	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteSession: &remote,
		RemoteName:    "myserver",
	}

	var b strings.Builder
	home.renderRemoteSessionItem(&b, item, false)
	rendered := b.String()

	if !strings.Contains(rendered, "remote-session") {
		t.Fatalf("expected the remote row to render its title; got: %q", rendered)
	}
	if strings.Contains(rendered, sentinelPaneTitle) {
		t.Fatalf("remote session row must not render a pane-title suffix "+
			"(parity deferred — see file-level comment). Found %q in: %q", sentinelPaneTitle, rendered)
	}
}
