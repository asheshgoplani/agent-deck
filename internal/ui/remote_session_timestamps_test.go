package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RemoteSession timestamp-badge parity is intentionally deferred.
//
// The session.RemoteSessionInfo struct (internal/session/ssh.go) carries
// only Tool, Status, CreatedAt (as a string), Title, Path, Group, and
// RemoteName. It does NOT carry LastStartedAt, hook events, or any
// per-session tmux state — those live on the remote machine, not over
// the wire to the local TUI.
//
// The local badge formula in pickBadgeTime layers over four signals:
// CreatedAt, LastStartedAt, hook UpdatedAt, and tmux-confirmed activity.
// For a remote row, three of those four are permanently unavailable.
// Wiring the badge with only CreatedAt would produce a "session age"
// readout that never updates — inconsistent with what users see for
// local rows and likely to be read as broken rather than helpful.
//
// Until the remote protocol is extended to ship at least one live
// activity signal (LastStartedAt or hook events), the badge stays
// local-only. This test pins that decision: it confirms the new
// timestamp output does NOT leak into remote rows, so a future change
// that silently adds the badge here will be forced through a
// conscious re-review.
//
// Tracking note for follow-up: extending wire format / RemoteSessionInfo
// to carry LastStartedAt + last hook event would be the minimum needed
// to make remote parity meaningful.
func TestRemoteSession_TimestampBadgeIsLocalOnly(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30
	home.showSessionTimestamps = true // would emit badge on local rows

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

	for _, sig := range []string{"ago", "just now"} {
		if strings.Contains(rendered, sig) {
			t.Fatalf("remote session row must not render the local-only timestamp badge "+
				"(parity deferred — see file-level comment). Found %q in: %q", sig, rendered)
		}
	}
}
