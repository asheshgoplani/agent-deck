package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Status-detection audit 2026-09-23: a remote row must never show a
// full-colour "running" when the controller cannot vouch for it. The classic
// row already went grey on a failed poll; the embedded card, the preview
// pane and the stale-by-age case still painted a green ●.
func newStaleRemoteHome(t *testing.T, embedded bool, pollStatus string, age time.Duration) (*Home, session.RemoteSessionInfo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	forceTrueColorProfile()
	h := newTestHomeWithItems(160, 40, nil)
	t.Cleanup(h.cancel)
	h.embeddedLayout = embedded
	ms := int64(100)
	h.remotePolls = map[string]session.RemotePollState{"dev": {LastPollStatus: pollStatus, LastPollMS: &ms}}
	h.remoteFromCache = map[string]bool{"dev": false}
	h.remoteFetchedAt = map[string]time.Time{"dev": time.Now().Add(-age)}
	rs := session.RemoteSessionInfo{ID: "s1", Title: "Remote work", Tool: "claude", Status: "running", RemoteName: "dev", Path: "/srv/x"}
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": {rs}}
	return h, rs
}

// liveRunningGlyph is the exact rendering a fresh running remote row gets.
func liveRunningGlyph() string {
	icon, style := remoteRowStatusGlyph("running", "", false)
	return style.Render(icon)
}

func remoteItem(rs *session.RemoteSessionInfo) session.Item {
	return session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "dev", RemoteSession: rs, Level: 1, IsLastInGroup: true}
}

func TestRemoteStaleRow_ClassicDimsGlyphWhenStaleByAge(t *testing.T) {
	h, rs := newStaleRemoteHome(t, false, "ok", 47*time.Second)
	var b strings.Builder
	h.renderRemoteSessionItem(&b, remoteItem(&rs), false)
	got := b.String()
	if !strings.Contains(stripAnsi(got), "status 47s old") {
		t.Fatalf("missing age marker:\n%s", stripAnsi(got))
	}
	if strings.Contains(got, liveRunningGlyph()) {
		t.Fatalf("stale row must not paint the live running glyph:\n%q", got)
	}
}

// embeddedCardWidth is a sidebar wide enough for the two-line card;
// renderRemoteSessionItem pins the width below embeddedCardMinWidth and so
// always draws the classic row.
const embeddedCardWidth = embeddedCardMinWidth + 32

func TestRemoteStaleRow_EmbeddedCardFollowsPollHealth(t *testing.T) {
	h, rs := newStaleRemoteHome(t, true, "timeout", 5*time.Second)
	var b strings.Builder
	h.renderRemoteSessionItemAtWidth(&b, remoteItem(&rs), false, embeddedCardWidth)
	got := stripAnsi(b.String())
	if !strings.Contains(got, "╰") {
		t.Fatalf("expected the two-line embedded card:\n%s", got)
	}
	if strings.Contains(got, "●") || !strings.Contains(got, "?") || !strings.Contains(got, "last known") {
		t.Fatalf("embedded card must show ? / last known on a failed poll:\n%s", got)
	}

	h2, rs2 := newStaleRemoteHome(t, true, "ok", 47*time.Second)
	b.Reset()
	h2.renderRemoteSessionItemAtWidth(&b, remoteItem(&rs2), false, embeddedCardWidth)
	if raw := b.String(); !strings.Contains(stripAnsi(raw), "status 47s old") || strings.Contains(raw, liveRunningGlyph()) {
		t.Fatalf("embedded card must dim and date a stale row:\n%s", stripAnsi(raw))
	}
}

func TestRemoteStaleRow_PreviewFollowsPollHealth(t *testing.T) {
	h, rs := newStaleRemoteHome(t, false, "host_down", 5*time.Second)
	got := stripAnsi(h.renderRemotePreview(remoteItem(&rs), 80, 20))
	if strings.Contains(got, "● running") || !strings.Contains(got, "? running (last known)") {
		t.Fatalf("preview must not claim a live running on a failed poll:\n%s", got)
	}
	h2, rs2 := newStaleRemoteHome(t, false, "ok", 47*time.Second)
	if got := stripAnsi(h2.renderRemotePreview(remoteItem(&rs2), 80, 20)); !strings.Contains(got, "running (status 47s old)") {
		t.Fatalf("preview must date a stale row:\n%s", got)
	}
}

// Review round 2 (P2-3): the stale-by-age rows the list now dims must not
// be counted as green running by the filter-bar pill or painted as a
// full-colour "● N" in the remote host / sub-group headers.
func TestRemoteStaleRow_FilterBarDoesNotCountStaleRunning(t *testing.T) {
	h, _ := newStaleRemoteHome(t, false, "ok", 47*time.Second)
	h.cachedStatusCounts.valid.Store(false)
	if running, _, _, _, _ := h.countSessionStatuses(); running != 0 {
		t.Fatalf("filter-bar running = %d, want 0 for a remote whose snapshot is 47s old", running)
	}

	fresh, _ := newStaleRemoteHome(t, false, "ok", 2*time.Second)
	fresh.cachedStatusCounts.valid.Store(false)
	if running, _, _, _, _ := fresh.countSessionStatuses(); running != 1 {
		t.Fatalf("filter-bar running = %d, want 1 for a fresh remote", running)
	}
}

func TestRemoteStaleRow_HeaderCountsDimmedWhenStale(t *testing.T) {
	liveCount := GroupStatusRunning.Render("● 1")
	for _, level := range []int{0, 1} {
		item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev", Level: level}
		if level > 0 {
			item.Path = "remotes/dev/work"
		}
		h, _ := newStaleRemoteHome(t, false, "ok", 47*time.Second)
		h.remoteSessions["dev"][0].Group = "work"
		var b strings.Builder
		h.renderRemoteGroupItem(&b, item, false, 0)
		got := b.String()
		if !strings.Contains(stripAnsi(got), "● 1") {
			t.Fatalf("level %d: stale header must still show its count:\n%s", level, stripAnsi(got))
		}
		if strings.Contains(got, liveCount) {
			t.Fatalf("level %d: stale header paints a full-colour green running count:\n%q", level, got)
		}

		fresh, _ := newStaleRemoteHome(t, false, "ok", 2*time.Second)
		fresh.remoteSessions["dev"][0].Group = "work"
		b.Reset()
		fresh.renderRemoteGroupItem(&b, item, false, 0)
		if !strings.Contains(b.String(), liveCount) {
			t.Fatalf("level %d: fresh header must keep the green running count:\n%q", level, b.String())
		}
	}
}
