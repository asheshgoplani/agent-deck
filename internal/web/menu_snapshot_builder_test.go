package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestBuildMenuSnapshotGroupDefaultPath pins the wire contract for
// MenuGroup.DefaultPath: it carries the group's EXPLICITLY configured
// default_path and is omitted entirely when none is set.
//
// The "no explicit default" case matters: session.Group also supports a
// derived most-recent-session fallback (DefaultPathForGroup), and shipping
// that would make the web unable to tell "configured" from "guessed". The
// client derives its own fallback from the group's newest session.
func TestBuildMenuSnapshotGroupDefaultPath(t *testing.T) {
	dir := t.TempDir()

	inst := session.NewInstanceWithGroupAndTool("api", "/srv/api", "work", "claude")
	inst.ID = "sess-api"
	inst.GroupPath = "work"

	groups := []*session.GroupData{
		{Name: "work", Path: "work", Expanded: true, Order: 0, DefaultPath: dir},
		{Name: "personal", Path: "personal", Expanded: true, Order: 1},
	}

	snap := BuildMenuSnapshot("default", []*session.Instance{inst}, groups, time.Now())

	byPath := map[string]*MenuGroup{}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeGroup && item.Group != nil {
			byPath[item.Group.Path] = item.Group
		}
	}

	work := byPath["work"]
	if work == nil {
		t.Fatalf("group %q missing from snapshot", "work")
	}
	if work.DefaultPath != dir {
		t.Errorf("work.DefaultPath = %q, want %q", work.DefaultPath, dir)
	}

	personal := byPath["personal"]
	if personal == nil {
		t.Fatalf("group %q missing from snapshot", "personal")
	}
	if personal.DefaultPath != "" {
		t.Errorf("personal.DefaultPath = %q, want empty (no explicit default configured)", personal.DefaultPath)
	}

	// omitempty: the key must not appear at all for an unconfigured group,
	// so clients can distinguish "unset" from "empty string".
	blob, err := json.Marshal(personal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(blob); strings.Contains(got, "defaultPath") {
		t.Errorf("unconfigured group serialized defaultPath key: %s", got)
	}
}
