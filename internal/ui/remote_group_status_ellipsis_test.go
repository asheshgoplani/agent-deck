package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestRemoteGroupHeaderTruncatesWithEllipsis is a regression test for
// finding 12 (UI audit 2026-09-18): the remote-group status text hard-cut
// without an ellipsis when it overflowed the panel width, because
// renderRemoteGroupItem never received a width budget at all (unlike
// session titles, which self-truncate with cellTruncate before assembly).
func TestRemoteGroupHeaderTruncatesWithEllipsis(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.remoteFromCache = map[string]bool{"agentbox": true} // "· cached, refreshing…" trailer

	item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "agentbox", Path: "remotes/agentbox", Level: 0}
	h.remoteHeaderCounts = map[string]remoteHeaderCount{"remotes/agentbox": {total: 45}}

	const narrowWidth = 40 // narrow enough that the trailer must be cut
	var b strings.Builder
	h.renderRemoteGroupItem(&b, item, false, narrowWidth)
	line := stripAnsi(strings.TrimRight(b.String(), "\n"))

	if cellWidth(line) > narrowWidth {
		t.Fatalf("remote group header line exceeds width budget %d: %d (%q)", narrowWidth, cellWidth(line), line)
	}
	// The full trailer is "cached, refreshing…"; a genuine cut must land
	// somewhere short of that and end in the ellipsis marker, not the
	// hard-cut bug's bare trailing punctuation (e.g. "...cached," with
	// nothing to show it was truncated).
	if strings.HasSuffix(line, "cached, refreshing…") {
		t.Fatalf("test width %d should have forced a real truncation, but the full trailer survived: %q", narrowWidth, line)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("remote group header should truncate with an ellipsis when cut, got %q", line)
	}
}

// TestRemoteGroupHeaderNoTruncationWhenWide checks that a zero/unbounded
// width (existing test convention) and a comfortably wide budget leave the
// row untouched.
func TestRemoteGroupHeaderNoTruncationWhenWide(t *testing.T) {
	h := NewHome()
	h.width, h.height = 200, 40
	h.remoteFromCache = map[string]bool{"agentbox": true}

	item := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "agentbox", Path: "remotes/agentbox", Level: 0}
	h.remoteHeaderCounts = map[string]remoteHeaderCount{"remotes/agentbox": {total: 45}}

	var b strings.Builder
	h.renderRemoteGroupItem(&b, item, false, 200)
	line := stripAnsi(strings.TrimRight(b.String(), "\n"))

	// "refreshing…" is the trailer's own content, not a truncation marker;
	// the row must survive intact at a width that comfortably fits it.
	if !strings.HasSuffix(line, "cached, refreshing…") {
		t.Errorf("expected the full 'cached, refreshing…' trailer to survive untruncated at a wide width, got %q", line)
	}
}
