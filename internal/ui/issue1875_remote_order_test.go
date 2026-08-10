// Issue #1875, part 2: the overlay itself.
//
// The reorder overlay is local, so it drifts against a remote that adds and
// removes sessions without asking. These tests pin the three drift rules —
// skip stale IDs, keep unseen sessions in their natural position, emit every
// fetched session exactly once — plus their interleaving, the per-remote and
// per-group scoping, and the ui_state round trip across a restart.
package ui

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestApplyRemoteSessionOrder_Drift_Issue1875(t *testing.T) {
	cases := []struct {
		name    string
		natural []string
		order   []string
		want    []string
	}{
		{
			name:    "empty overlay keeps the fetched order",
			natural: []string{"a", "b", "c"},
			order:   nil,
			want:    []string{"a", "b", "c"},
		},
		{
			name:    "empty remote yields nothing",
			natural: nil,
			order:   []string{"a", "b"},
			want:    nil,
		},
		{
			name:    "single session cannot move",
			natural: []string{"a"},
			order:   []string{"b", "a"},
			want:    []string{"a"},
		},
		{
			name:    "full overlay is honored exactly",
			natural: []string{"a", "b", "c"},
			order:   []string{"c", "a", "b"},
			want:    []string{"c", "a", "b"},
		},
		{
			name:    "stale ids are skipped",
			natural: []string{"a", "b", "c"},
			order:   []string{"c", "deleted-on-the-remote", "a", "b"},
			want:    []string{"c", "a", "b"},
		},
		{
			name:    "an entirely stale overlay is inert",
			natural: []string{"a", "b"},
			order:   []string{"x", "y"},
			want:    []string{"a", "b"},
		},
		{
			name: "unseen sessions keep their natural position",
			// "c" is new on the remote and sits last there; it must stay last
			// rather than be dragged along by the overlay, and "d" must stay
			// between the two overlay-known rows.
			natural: []string{"a", "d", "b", "c"},
			order:   []string{"b", "a"},
			want:    []string{"b", "d", "a", "c"},
		},
		{
			name:    "stale and unseen interleaved",
			natural: []string{"a", "new1", "b", "new2", "c"},
			order:   []string{"c", "gone1", "a", "gone2", "b"},
			want:    []string{"c", "new1", "a", "new2", "b"},
		},
		{
			name:    "an overlay repeat is used once",
			natural: []string{"a", "b", "c"},
			order:   []string{"b", "b", "a"},
			want:    []string{"b", "a", "c"},
		},
		{
			name: "duplicate ids on the remote leave the fetched order alone",
			// Cannot map slots 1:1, and dropping or doubling a row is worse
			// than not reordering.
			natural: []string{"a", "a", "b"},
			order:   []string{"b", "a"},
			want:    []string{"a", "a", "b"},
		},
		{
			name:    "an id-less row is never addressable but is still emitted",
			natural: []string{"a", "", "b"},
			order:   []string{"b", "a"},
			want:    []string{"b", "", "a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			natural := append([]string(nil), tc.natural...)
			got := applyRemoteSessionOrder(natural, tc.order)

			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			// The result must always be a permutation of the input: no row
			// dropped, none doubled.
			gotSorted, wantSorted := append([]string(nil), got...), append([]string(nil), tc.natural...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if strings.Join(gotSorted, ",") != strings.Join(wantSorted, ",") {
				t.Fatalf("not a permutation of the fetched list: %v vs %v", gotSorted, wantSorted)
			}
			// The caller's slice must survive untouched.
			if strings.Join(natural, ",") != strings.Join(tc.natural, ",") {
				t.Fatalf("input slice was mutated: %v", natural)
			}
		})
	}
}

// The overlay is keyed per remote AND per group path, and its values are
// session IDs, so a same-named group on another host and an identically titled
// session on another host both stay out of each other's way.
func TestRemoteOrderScoping_Issue1875(t *testing.T) {
	devSessions := []session.RemoteSessionInfo{
		{ID: "dev-1", Title: "build", Group: "work"},
		{ID: "dev-2", Title: "test", Group: "work"},
		{ID: "dev-3", Title: "build", Group: "other"},
		{ID: "dev-4", Title: "test", Group: "other"},
	}
	prodSessions := []session.RemoteSessionInfo{
		{ID: "prod-1", Title: "build", Group: "work"},
		{ID: "prod-2", Title: "test", Group: "work"},
	}

	// Only dev's "work" bucket is reordered.
	order := map[string][]string{
		remoteOrderKey("dev", "work"): {"dev-2", "dev-1"},
	}

	// Group buckets are emitted in lexicographic path order ("other" before
	// "work"), which the session overlay must not disturb; inside "work" the
	// overlay applies, and "other" keeps the fetched order.
	devIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", devSessions, remoteHealth{}, order))
	if strings.Join(devIDs, ",") != "dev-3,dev-4,dev-2,dev-1" {
		t.Fatalf("dev order = %v, want [dev-3 dev-4 dev-2 dev-1]", devIDs)
	}

	prodIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("prod", prodSessions, remoteHealth{}, order))
	if strings.Join(prodIDs, ",") != "prod-1,prod-2" {
		t.Fatalf("dev's overlay leaked onto prod: order = %v, want [prod-1 prod-2]", prodIDs)
	}

	// An overlay written against prod's same-named group must not move dev.
	order[remoteOrderKey("prod", "work")] = []string{"prod-2", "prod-1"}
	devIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", devSessions, remoteHealth{}, order))
	if strings.Join(devIDs, ",") != "dev-3,dev-4,dev-2,dev-1" {
		t.Fatalf("prod's overlay leaked onto dev: order = %v", devIDs)
	}
	prodIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("prod", prodSessions, remoteHealth{}, order))
	if strings.Join(prodIDs, ",") != "prod-2,prod-1" {
		t.Fatalf("prod order = %v, want [prod-2 prod-1]", prodIDs)
	}

	// Every fetched session is still emitted exactly once, and no group header
	// was disturbed by the session reorder.
	if len(devIDs) != len(devSessions) {
		t.Fatalf("emitted %d rows for %d sessions", len(devIDs), len(devSessions))
	}
	var headers []string
	for _, it := range buildRemoteFlatItemsOrdered("dev", devSessions, remoteHealth{}, order) {
		if it.Type == session.ItemTypeRemoteGroup {
			headers = append(headers, it.Path)
		}
	}
	if strings.Join(headers, ",") != "remotes/dev,remotes/dev/other,remotes/dev/work" {
		t.Fatalf("group headers moved: %v", headers)
	}
}

func remoteItemSessionIDs(items []session.Item) []string {
	var ids []string
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil {
			ids = append(ids, it.RemoteSession.ID)
		}
	}
	return ids
}

// The order is a view preference, so it must survive a TUI restart the same
// way the preview mode and status filter do: written to ui_state, read back on
// the next launch.
func TestRemoteOrderSurvivesRestart_Issue1875(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i1875order")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	sessions := []session.RemoteSessionInfo{
		{ID: "s-alpha", Title: "alpha", Group: "work"},
		{ID: "s-beta", Title: "beta", Group: "work"},
		{ID: "s-gamma", Title: "gamma", Group: "work"},
	}
	key := remoteOrderKey("dev", "work")

	before := NewHome()
	before.storage = storage
	before.profile = "_i1875order"
	before.storageWatcher = nil
	before.remoteSessionOrder[key] = []string{"s-gamma", "s-alpha", "s-beta"}
	before.saveUIState()

	// A fresh process: new Home, same on-disk state.
	after := NewHome()
	after.storage = storage
	after.profile = "_i1875order"
	after.storageWatcher = nil
	after.loadUIState()

	got := after.remoteSessionOrder[key]
	if strings.Join(got, ",") != "s-gamma,s-alpha,s-beta" {
		t.Fatalf("order did not survive the restart: %v, want [s-gamma s-alpha s-beta]", got)
	}

	// And it must still produce that order on screen.
	ids := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", sessions, remoteHealth{}, after.remoteSessionOrder))
	if strings.Join(ids, ",") != "s-gamma,s-alpha,s-beta" {
		t.Fatalf("restored overlay did not reorder the rows: %v", ids)
	}
}

// End to end through the key handler: a shift+up must be persisted, so the
// order the user set is the order the next launch shows.
func TestShiftUpOnRemoteSessionPersists_Issue1875(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i1875key")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := armHomeForRemoteReorder(t)
	h.storage = storage
	h.profile = "_i1875key"
	h.storageWatcher = nil

	putCursorOnRemoteSession(t, h, "s-gamma")
	h.moveRemoteItem(h.flatItems[h.cursor], -1)

	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-alpha,s-gamma,s-beta" {
		t.Fatalf("in-memory order = %v, want [s-alpha s-gamma s-beta]", got)
	}

	reloaded := NewHome()
	reloaded.storage = storage
	reloaded.profile = "_i1875key"
	reloaded.storageWatcher = nil
	reloaded.loadUIState()

	got := reloaded.remoteSessionOrder[remoteOrderKey("dev", "work")]
	if strings.Join(got, ",") != "s-alpha,s-gamma,s-beta" {
		t.Fatalf("the reorder was not persisted: %v, want [s-alpha s-gamma s-beta]", got)
	}
}
