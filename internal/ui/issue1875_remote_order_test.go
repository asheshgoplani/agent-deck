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

	tea "github.com/charmbracelet/bubbletea"

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
	order := remoteOrder{"dev": {"work": {"dev-2", "dev-1"}}}

	// Group buckets are emitted in lexicographic path order ("other" before
	// "work"), which the session overlay must not disturb; inside "work" the
	// overlay applies, and "other" keeps the fetched order.
	devIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", devSessions, nil, order.forRemote("dev")))
	if strings.Join(devIDs, ",") != "dev-3,dev-4,dev-2,dev-1" {
		t.Fatalf("dev order = %v, want [dev-3 dev-4 dev-2 dev-1]", devIDs)
	}

	prodIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("prod", prodSessions, nil, order.forRemote("prod")))
	if strings.Join(prodIDs, ",") != "prod-1,prod-2" {
		t.Fatalf("dev's overlay leaked onto prod: order = %v, want [prod-1 prod-2]", prodIDs)
	}

	// An overlay written against prod's same-named group must not move dev.
	order.set("prod", "work", []string{"prod-2", "prod-1"})
	devIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", devSessions, nil, order.forRemote("dev")))
	if strings.Join(devIDs, ",") != "dev-3,dev-4,dev-2,dev-1" {
		t.Fatalf("prod's overlay leaked onto dev: order = %v", devIDs)
	}
	prodIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("prod", prodSessions, nil, order.forRemote("prod")))
	if strings.Join(prodIDs, ",") != "prod-2,prod-1" {
		t.Fatalf("prod order = %v, want [prod-2 prod-1]", prodIDs)
	}

	// Every fetched session is still emitted exactly once, and no group header
	// was disturbed by the session reorder.
	if len(devIDs) != len(devSessions) {
		t.Fatalf("emitted %d rows for %d sessions", len(devIDs), len(devSessions))
	}
	var headers []string
	for _, it := range buildRemoteFlatItemsOrdered("dev", devSessions, nil, order.forRemote("dev")) {
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
	before := NewHome()
	before.storage = storage
	before.profile = "_i1875order"
	before.storageWatcher = nil
	before.remoteSessionOrder.set("dev", "work", []string{"s-gamma", "s-alpha", "s-beta"})
	if err := before.saveUIStateErr(); err != nil {
		t.Fatalf("saveUIStateErr: %v", err)
	}

	// A fresh process: new Home, same on-disk state.
	after := NewHome()
	after.storage = storage
	after.profile = "_i1875order"
	after.storageWatcher = nil
	after.loadUIState()

	got := after.remoteSessionOrder.forRemote("dev")["work"]
	if strings.Join(got, ",") != "s-gamma,s-alpha,s-beta" {
		t.Fatalf("order did not survive the restart: %v, want [s-gamma s-alpha s-beta]", got)
	}

	// And it must still produce that order on screen.
	ids := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", sessions, nil, after.remoteSessionOrder.forRemote("dev")))
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

	got := reloaded.remoteSessionOrder.forRemote("dev")["work"]
	if strings.Join(got, ",") != "s-alpha,s-gamma,s-beta" {
		t.Fatalf("the reorder was not persisted: %v, want [s-alpha s-gamma s-beta]", got)
	}
}

// Review round 1, F1: the overlay was keyed by concatenating
// "remotes/<remote>/<group>". Remote names are unrestricted TOML map keys and
// group paths legitimately contain "/", so remote "dev/work" + group "x" and
// remote "dev" + group "work/x" produced the same key and shared an ordering.
// The nested map cannot alias whatever the names contain.
func TestRemoteOrderKeysCannotAlias_Issue1875(t *testing.T) {
	// Bucket A: a remote whose NAME contains a slash, group "x".
	aSessions := []session.RemoteSessionInfo{
		{ID: "a-1", Title: "one", Group: "x"},
		{ID: "a-2", Title: "two", Group: "x"},
	}
	// Bucket B: a remote named "dev", group path "work/x". Under the old
	// concatenated key both of these were "remotes/dev/work/x".
	bSessions := []session.RemoteSessionInfo{
		{ID: "b-1", Title: "one", Group: "work/x"},
		{ID: "b-2", Title: "two", Group: "work/x"},
	}

	order := remoteOrder{}
	order.set("dev/work", "x", []string{"a-2", "a-1"})

	aIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev/work", aSessions, nil, order.forRemote("dev/work")))
	if strings.Join(aIDs, ",") != "a-2,a-1" {
		t.Fatalf("remote 'dev/work' group 'x' order = %v, want [a-2 a-1]", aIDs)
	}

	bIDs := remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", bSessions, nil, order.forRemote("dev")))
	if strings.Join(bIDs, ",") != "b-1,b-2" {
		t.Fatalf("the 'dev/work'+'x' ordering aliased onto 'dev'+'work/x': order = %v, want [b-1 b-2]", bIDs)
	}

	// And the reverse: an ordering on dev/work/x must not reach dev/work + x.
	order.set("dev", "work/x", []string{"b-2", "b-1"})
	aIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev/work", aSessions, nil, order.forRemote("dev/work")))
	if strings.Join(aIDs, ",") != "a-2,a-1" {
		t.Fatalf("the 'dev'+'work/x' ordering aliased onto 'dev/work'+'x': order = %v, want [a-2 a-1]", aIDs)
	}
	bIDs = remoteItemSessionIDs(buildRemoteFlatItemsOrdered("dev", bSessions, nil, order.forRemote("dev")))
	if strings.Join(bIDs, ",") != "b-2,b-1" {
		t.Fatalf("remote 'dev' group 'work/x' order = %v, want [b-2 b-1]", bIDs)
	}
}

// F1, persistence half: the separator-bearing names must also survive the
// ui_state round trip as distinct buckets.
func TestRemoteOrderSlashKeysRoundTrip_Issue1875(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i1875slash")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	before := NewHome()
	before.storage = storage
	before.profile = "_i1875slash"
	before.storageWatcher = nil
	before.remoteSessionOrder.set("dev/work", "x", []string{"a-2", "a-1"})
	before.remoteSessionOrder.set("dev", "work/x", []string{"b-2", "b-1"})
	if err := before.saveUIStateErr(); err != nil {
		t.Fatalf("saveUIStateErr: %v", err)
	}

	after := NewHome()
	after.storage = storage
	after.profile = "_i1875slash"
	after.storageWatcher = nil
	after.loadUIState()

	if got := after.remoteSessionOrder.forRemote("dev/work")["x"]; strings.Join(got, ",") != "a-2,a-1" {
		t.Fatalf("remote 'dev/work' group 'x' = %v, want [a-2 a-1]", got)
	}
	if got := after.remoteSessionOrder.forRemote("dev")["work/x"]; strings.Join(got, ",") != "b-2,b-1" {
		t.Fatalf("remote 'dev' group 'work/x' = %v, want [b-2 b-1]", got)
	}
	if got := after.remoteSessionOrder.forRemote("dev")["x"]; got != nil {
		t.Fatalf("buckets merged across the round trip: dev/x = %v, want nothing", got)
	}
}

// Review round 1, F2: saveUIState only logged SetMeta failures, so a reorder on
// a locked, read-only or full database moved the rows on screen, said nothing,
// and reverted at the next launch. The failure is injected by closing the
// storage out from under the handler, which is what a dead DB handle looks like
// to SetMeta.
func TestRemoteReorderReportsSaveFailure_Issue1875(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i1875savefail")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}

	h := armHomeForRemoteReorder(t)
	h.storage = storage
	h.profile = "_i1875savefail"
	h.storageWatcher = nil

	// Sanity: the same move succeeds quietly while the DB is alive.
	putCursorOnRemoteSession(t, h, "s-beta")
	h.moveRemoteItem(h.flatItems[h.cursor], -1)
	if h.err != nil {
		t.Fatalf("a healthy save reported an error: %v", h.err)
	}

	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := h.saveUIStateErr(); got == nil {
		t.Fatal("precondition: saveUIStateErr returned nil against a closed database")
	}

	h.clearError()
	putCursorOnRemoteSession(t, h, "s-gamma")
	h.moveRemoteItem(h.flatItems[h.cursor], -1)

	if h.err == nil {
		t.Fatal("a reorder that could not be persisted reported success")
	}
	msg := strings.ToLower(h.err.Error())
	if !strings.Contains(msg, "saved") && !strings.Contains(msg, "save") {
		t.Fatalf("message does not say the order was not saved: %q", h.err.Error())
	}
	if !strings.Contains(msg, "restart") {
		t.Fatalf("message does not warn that the order will not survive a restart: %q", h.err.Error())
	}

	// The move itself still happened on screen — the message explains that it
	// is not durable, it does not pretend the keystroke was refused.
	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != "s-beta,s-gamma,s-alpha" {
		t.Fatalf("in-memory order = %v, want [s-beta s-gamma s-alpha]", got)
	}
}

// Review round 2, F1: orderRemoteBucket deliberately refuses to permute a
// bucket whose IDs are not unique, so a move there could never change the
// screen. moveRemoteItem used to swap and save anyway — nothing moved, no
// message, which is the same silent no-op this issue exists to fix, reached by
// a different door. The bail-out is correct; treating it as success was not.
//
// This drives the key handler, not just the overlay function.
func TestRemoteReorderReportsDuplicateIDs_Issue1875(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)

	newHome := func() *Home {
		h := NewHome()
		h.width, h.height = 160, 40
		h.initialLoading = false
		h.remoteSessions = map[string][]session.RemoteSessionInfo{
			"dev": {
				{ID: "dup", Title: "first", Status: "idle", Tool: "claude", Group: "work"},
				{ID: "dup", Title: "second", Status: "idle", Tool: "claude", Group: "work"},
				{ID: "s-b", Title: "third", Status: "idle", Tool: "claude", Group: "work"},
			},
		}
		h.rebuildFlatItems()
		return h
	}

	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "shift+down", key: tea.KeyMsg{Type: tea.KeyShiftDown}},
		{name: "shift+up", key: tea.KeyMsg{Type: tea.KeyShiftUp}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHome()
			h.clearError()

			before := visibleRemoteSessionIDs(h, "dev")
			putCursorOnRemoteSession(t, h, "dup")
			h.handleMainKey(tc.key)

			// The overlay cannot express this bucket, so the rows must not move.
			after := visibleRemoteSessionIDs(h, "dev")
			if strings.Join(after, ",") != strings.Join(before, ",") {
				t.Fatalf("rows moved in an unaddressable bucket: %v -> %v", before, after)
			}
			// ...and the user must be told why, rather than see a dead key.
			if h.err == nil {
				t.Fatal("silent no-op: a duplicate-id bucket produced no footer message")
			}
			msg := h.err.Error()
			if !strings.Contains(msg, "dup") {
				t.Fatalf("message does not name the duplicated id: %q", msg)
			}
			if !strings.Contains(strings.ToLower(msg), "more than one") {
				t.Fatalf("message does not explain the duplication: %q", msg)
			}
			// Nothing may be written for a bucket that cannot be ordered.
			if got := h.remoteSessionOrder.forRemote("dev")["work"]; got != nil {
				t.Fatalf("an order was stored for an unaddressable bucket: %v", got)
			}
		})
	}

	// The third session in that group is equally stuck: any order stored for
	// the bucket would be ignored by the builder, so it must report too rather
	// than appear to work.
	h := newHome()
	h.clearError()
	before := visibleRemoteSessionIDs(h, "dev")
	putCursorOnRemoteSession(t, h, "s-b")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftUp})
	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("rows moved in an unaddressable bucket: %v -> %v", before, got)
	}
	if h.err == nil {
		t.Fatal("silent no-op: moving a uniquely-named row in a duplicate-id bucket said nothing")
	}
}

func TestRemoteReorderRejectsBucketWithMissingID(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "s-a", Title: "first", Status: "idle", Tool: "claude", Group: "work"},
			{ID: "", Title: "legacy", Status: "idle", Tool: "claude", Group: "work"},
			{ID: "s-b", Title: "third", Status: "idle", Tool: "claude", Group: "work"},
		},
	}
	h.rebuildFlatItems()
	before := visibleRemoteSessionIDs(h, "dev")
	putCursorOnRemoteSession(t, h, "s-b")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftUp})
	if got := visibleRemoteSessionIDs(h, "dev"); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("rows moved across an ID-less row: %v -> %v", before, got)
	}
	if h.err == nil || !strings.Contains(strings.ToLower(h.err.Error()), "without an id") {
		t.Fatalf("missing explicit ID-less-row error: %v", h.err)
	}
	if got := h.remoteSessionOrder.forRemote("dev")["work"]; got != nil {
		t.Fatalf("stored unrepresentable order: %v", got)
	}
}
