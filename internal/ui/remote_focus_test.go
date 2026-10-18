package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// remoteFocusSessions is remote "dev" for the #2704 tests:
//
//	remotes/dev/work      -> run-1 (running), stop-1 (stopped), old-1 (idle, 60 days)
//	remotes/dev/work/api  -> api-1 (idle)
//	archived              -> arch-1
func remoteFocusSessions() []session.RemoteSessionInfo {
	recent := time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	old := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)
	return []session.RemoteSessionInfo{
		{ID: "run-1", Title: "run-1", Group: "work", Status: "running", Tool: "claude", LastActivityAt: recent},
		{ID: "stop-1", Title: "stop-1", Group: "work", Status: "stopped", Tool: "claude", LastActivityAt: recent},
		{ID: "old-1", Title: "old-1", Group: "work", Status: "idle", Tool: "claude", LastActivityAt: old},
		{ID: "api-1", Title: "api-1", Group: "work/api", Status: "idle", Tool: "claude", LastActivityAt: recent},
		{ID: "arch-1", Title: "arch-1", Group: "work", Status: "idle", Tool: "claude", Archived: true, LastActivityAt: recent},
	}
}

// buildRemoteFocusHome is buildFocusHome plus remote "dev" with every remote
// header folded shut, so a focus has to unfold them.
func buildRemoteFocusHome(t *testing.T) (*Home, []*session.Instance) {
	t.Helper()
	home, insts := buildFocusHome(t)
	home.storage = nil // keep fold saves out of the shared test profile
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": remoteFocusSessions()}
	home.remoteGroupsCollapsed = map[string]bool{
		"remotes/dev":          true,
		"remotes/dev/work":     true,
		"remotes/dev/work/api": true,
	}
	home.cursor = 0
	home.rebuildFlatItems()
	return home, insts
}

func writeRemoteFocus(t *testing.T, db *statedb.StateDB, remote, id string, attach bool, ts time.Time) {
	t.Helper()
	fr := session.FocusRequest{ID: id, TS: ts.UnixNano(), Attach: attach, Remote: remote}
	if err := session.WriteFocusRequestFull(db, fr); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func localCount(h *Home) int {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	return len(h.instances)
}

func cursorRemoteID(h *Home) string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	it := h.flatItems[h.cursor]
	if it.Type != session.ItemTypeRemoteSession || it.RemoteSession == nil {
		return ""
	}
	return it.RemoteName + "/" + it.RemoteSession.ID
}

// cmdOutcome runs cmd and describes what it produced, so the focus path can be
// compared with the Enter key: both build closures, never comparable values.
func cmdOutcome(cmd tea.Cmd) string {
	if cmd == nil {
		return "<nil>"
	}
	errText := func(err error) string {
		if err == nil {
			return "<nil>"
		}
		return err.Error()
	}
	switch m := cmd().(type) {
	case remoteSessionCreatedMsg:
		return "attach: " + errText(m.err)
	case remoteSessionRestartedMsg:
		return fmt.Sprintf("restart %s/%s: %s", m.remoteName, m.sessionID, errText(m.err))
	default:
		return fmt.Sprintf("%T", m)
	}
}

func TestRemoteFocus_SelectOnlyUnfoldsAndSelects(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	before := localCount(home)
	if home.remoteFlatItemIndex("dev", "api-1") >= 0 {
		t.Fatal("precondition: api-1 must be folded away")
	}

	writeRemoteFocus(t, db, "dev", "api-1", false, time.Now())
	if cmd := home.consumeFocusRequest(db); cmd != nil {
		t.Fatal("select-only remote focus must not return a cmd")
	}
	if got := cursorRemoteID(home); got != "dev/api-1" {
		t.Fatalf("cursor on %q, want dev/api-1", got)
	}
	for _, p := range []string{"remotes/dev", "remotes/dev/work", "remotes/dev/work/api"} {
		if home.remoteGroupsCollapsed[p] {
			t.Errorf("header %s still folded", p)
		}
	}
	if localCount(home) != before {
		t.Fatalf("local sessions changed: %d -> %d", before, localCount(home))
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("request not cleared: %q", raw)
	}
	if home.pendingRemoteFocus != nil {
		t.Fatal("a selected request must not stay pending")
	}
}

func TestRemoteFocus_UnknownOrArchivedLeavesCursor(t *testing.T) {
	cases := []struct{ name, remote, id string }{
		{"unknown remote", "nope", "run-1"},
		{"unknown id", "dev", "missing"},
		{"archived id", "dev", "arch-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := buildRemoteFocusHome(t)
			home.remoteConfigured = map[string]struct{}{"dev": {}}
			db := newUITempStateDB(t)
			home.cursor = 2
			writeRemoteFocus(t, db, tc.remote, tc.id, true, time.Now())
			// No cmd in any case: the tick only returns early for an attach.
			if cmd := home.consumeFocusRequest(db); cmd != nil {
				t.Fatal("no cmd expected")
			}
			if home.cursor != 2 {
				t.Fatalf("cursor moved to %d", home.cursor)
			}
			if tc.id == "missing" {
				if home.pendingRemoteFocus == nil {
					t.Fatal("an unknown id waits for the next refresh")
				}
				return
			}
			if home.pendingRemoteFocus != nil {
				t.Fatal("unknown remote / archived id must not stay pending")
			}
		})
	}
}

// The focus path must produce what Enter on the same row produces, in the
// classic layout (SSH attach) and the embedded layout (stopped → restart).
func TestRemoteFocus_AttachMatchesEnter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		embedded bool
		id       string
		want     string // pinned outcome: "dev" is not in the test config
	}{
		{"classic running", false, "run-1", "attach: cannot attach on dev: "},
		{"classic stopped", false, "stop-1", "attach: cannot attach on dev: "},
		{"embedded stopped restarts", true, "stop-1", "restart dev/stop-1: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enterHome, _ := buildRemoteFocusHome(t)
			enterHome.embeddedLayout = tc.embedded
			if !enterHome.SelectRemoteSession("dev", tc.id) {
				t.Fatal("select failed")
			}
			_, enterCmd := enterHome.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})

			home, _ := buildRemoteFocusHome(t)
			home.embeddedLayout = tc.embedded
			db := newUITempStateDB(t)
			writeRemoteFocus(t, db, "dev", tc.id, true, time.Now())
			focusCmd := home.consumeFocusRequest(db)
			if focusCmd == nil {
				t.Fatal("--attach must return the open cmd")
			}
			if got, want := cmdOutcome(focusCmd), cmdOutcome(enterCmd); got != want {
				t.Fatalf("focus produced %s, Enter produced %s", got, want)
			}
			if got := cmdOutcome(focusCmd); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("outcome %q, want prefix %q", got, tc.want)
			}
		})
	}
}

// Embedded layout already showing one remote session: a focus on another
// opens that one in the pane.
func TestRemoteFocus_EmbeddedSwitchesSession(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	home.embeddedLayout = true
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return &fakeInsertKeySender{}, nil
	}
	if !home.SelectRemoteSession("dev", "run-1") || !home.enterEmbeddedMode() {
		t.Fatal("precondition: embed run-1")
	}
	if !home.embeddedRemoteSessionIs("dev", "run-1") {
		t.Fatal("precondition: run-1 embedded")
	}
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "api-1", true, time.Now())
	if cmd := home.consumeFocusRequest(db); cmd == nil {
		t.Fatal("expected the embedded open cmd")
	}
	if !home.embeddedRemoteSessionIs("dev", "api-1") {
		t.Fatalf("embedded target = %s/%s, want dev/api-1", home.insertModeRemoteName, home.insertModeRemoteID)
	}
}

// A session the TUI has not fetched yet is selected once a refresh brings it.
func TestRemoteFocus_PendingUntilRefresh(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "new-1", false, time.Now())
	home.consumeFocusRequest(db)
	if home.pendingRemoteFocus == nil {
		t.Fatal("request for an unfetched session must wait")
	}
	if raw, _ := session.ReadFocusRequest(db); raw != "" {
		t.Fatalf("a pending request must never be written back: %q", raw)
	}

	sessions := append(remoteFocusSessions(), session.RemoteSessionInfo{ID: "new-1", Title: "new-1", Group: "work/api", Status: "running"})
	home.Update(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{"dev": sessions}})
	if got := cursorRemoteID(home); got != "dev/new-1" {
		t.Fatalf("cursor on %q after refresh, want dev/new-1", got)
	}
	if home.pendingRemoteFocus != nil {
		t.Fatal("pending request must be consumed")
	}
}

func TestRemoteFocus_PendingExpires(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "new-1", false, time.Now())
	home.consumeFocusRequest(db)
	before := home.cursor

	home.remoteSessions["dev"] = append(remoteFocusSessions(), session.RemoteSessionInfo{ID: "new-1", Title: "new-1", Group: "work", Status: "running"})
	home.rebuildFlatItems()
	if cmd := home.retryPendingRemoteFocus(time.Now().Add(session.FocusRequestTTL + time.Second)); cmd != nil {
		t.Fatal("expired request returned a cmd")
	}
	if home.cursor != before || home.pendingRemoteFocus != nil {
		t.Fatalf("expired request acted: cursor %d -> %d, pending=%v", before, home.cursor, home.pendingRemoteFocus)
	}
}

// The same through a real refresh: a request that is just past its lifetime
// when the session arrives is not acted on.
func TestRemoteFocus_PendingExpiresThroughRefresh(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "new-1", false, time.Now())
	home.consumeFocusRequest(db)
	if home.pendingRemoteFocus == nil {
		t.Fatal("precondition: pending")
	}
	before := home.cursor
	// Expire it outright rather than racing the wall clock.
	home.pendingRemoteFocus.deadline = time.Now().Add(-time.Second)
	sessions := append(remoteFocusSessions(), session.RemoteSessionInfo{ID: "new-1", Title: "new-1", Group: "work", Status: "running"})
	home.Update(remoteSessionsFetchedMsg{sessions: map[string][]session.RemoteSessionInfo{"dev": sessions}})
	if cursorRemoteID(home) == "dev/new-1" || home.pendingRemoteFocus != nil {
		t.Fatalf("expired request acted: cursor %d -> %d, pending=%v", before, home.cursor, home.pendingRemoteFocus)
	}
}

func TestRemoteFocus_NewerRequestReplacesPending(t *testing.T) {
	home, insts := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "new-1", false, time.Now())
	home.consumeFocusRequest(db)

	writeRemoteFocus(t, db, "dev", "new-2", false, time.Now())
	home.consumeFocusRequest(db)
	if p := home.pendingRemoteFocus; p == nil || p.id != "new-2" {
		t.Fatalf("pending = %+v, want new-2", p)
	}

	if err := session.WriteFocusRequest(db, insts[3].ID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	home.consumeFocusRequest(db)
	if home.pendingRemoteFocus != nil {
		t.Fatal("a local request must replace the pending remote one")
	}
}

func TestRemoteFocus_RevealsTimeFilteredRow(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	home.remoteGroupsCollapsed = map[string]bool{}
	home.timeFilter = session.TimeFilter7Days
	home.rebuildFlatItems()
	if home.timeFilter != session.TimeFilter7Days {
		t.Fatal("precondition: the filter must survive (recent rows match)")
	}
	if home.remoteFlatItemIndex("dev", "old-1") >= 0 {
		t.Fatal("precondition: old-1 hidden by the time filter")
	}
	if !home.SelectRemoteSession("dev", "old-1") {
		t.Fatal("select failed")
	}
	if home.timeFilter != session.TimeFilterAll || cursorRemoteID(home) != "dev/old-1" {
		t.Fatalf("timeFilter=%v cursor=%q", home.timeFilter, cursorRemoteID(home))
	}
}

func TestRemoteFocus_RevealsStatusFilteredRow(t *testing.T) {
	home, insts := buildRemoteFocusHome(t)
	insts[0].Status = session.StatusRunning
	home.statusFilter = session.StatusRunning
	home.rebuildFlatItems()
	if !home.SelectRemoteSession("dev", "api-1") {
		t.Fatal("select failed")
	}
	if home.statusFilter != "" || cursorRemoteID(home) != "dev/api-1" {
		t.Fatalf("statusFilter=%q cursor=%q", home.statusFilter, cursorRemoteID(home))
	}
}

// Local focus is unchanged when remotes are present, and never touches the
// remote folds.
func TestRemoteFocus_LocalFocusUnchanged(t *testing.T) {
	home, insts := buildRemoteFocusHome(t)
	db := newUITempStateDB(t)
	if err := session.WriteFocusRequest(db, insts[2].ID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if cmd := home.consumeFocusRequest(db); cmd != nil {
		t.Fatal("select-only local focus returned a cmd")
	}
	if idx := home.flatItemIndexByID(insts[2].ID); idx < 0 || home.cursor != idx {
		t.Fatalf("cursor=%d idx=%d", home.cursor, idx)
	}
	if !home.remoteGroupsCollapsed["remotes/dev"] {
		t.Fatal("local focus must not unfold remotes")
	}
}

// Golden frames of the list before and after `session focus --remote dev
// api-1`: folded headers open and the cursor sits on the row.
func TestRemoteFocus_GoldenFrames(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	for i, it := range home.flatItems {
		if it.Type == session.ItemTypeRemoteGroup && it.Path == "remotes/dev" {
			home.cursor = i
		}
	}
	assertGolden(t, "remote_focus_before", home.renderSessionList(60, 20))
	db := newUITempStateDB(t)
	writeRemoteFocus(t, db, "dev", "api-1", false, time.Now())
	home.consumeFocusRequest(db)
	assertGolden(t, "remote_focus_after", home.renderSessionList(60, 20))
}

// An older TUI reads a remote request with the pre-#2704 struct (no remote
// field): the remote id is an unknown local id, so it is dropped.
func TestRemoteFocus_OldDecoderIgnoresRemoteRequest(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	before := home.cursor
	val, err := session.EncodeFocusRequestFull(session.FocusRequest{ID: "run-1", TS: time.Now().UnixNano(), Attach: true, Remote: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		ID     string `json:"id"`
		TS     int64  `json:"ts"`
		Attach bool   `json:"attach,omitempty"`
	}
	if err := json.Unmarshal([]byte(val), &old); err != nil {
		t.Fatalf("old struct cannot read a remote request: %v", err)
	}
	if old.ID != "run-1" || home.SelectSessionByID(old.ID) {
		t.Fatalf("old path selected remote id %q", old.ID)
	}
	if home.cursor != before {
		t.Fatalf("cursor moved %d -> %d", before, home.cursor)
	}
}

// The archived view shows archived rows, but focus never selects one.
func TestRemoteFocus_ArchivedRowNotSelectedEvenWhenShown(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	home.remoteGroupsCollapsed = map[string]bool{}
	home.statusFilter = FilterModeArchived
	home.rebuildFlatItems()
	if home.remoteFlatItemIndex("dev", "arch-1") < 0 {
		t.Fatal("precondition: arch-1 shown in the archived view")
	}
	before := home.cursor
	if home.SelectRemoteSession("dev", "arch-1") || home.cursor != before {
		t.Fatalf("archived row selected (cursor %d -> %d)", before, home.cursor)
	}
}

// A waiting request asks the tick for a fetch every remoteFocusRetryInterval
// until its TTL ends, never while a fetch is in flight.
func TestRemoteFocus_WaitingRequestRetriesFetch(t *testing.T) {
	home, _ := buildRemoteFocusHome(t)
	now := time.Now()
	if home.remoteFocusWantsFetch(now) {
		t.Fatal("no request, no fetch")
	}
	home.pendingRemoteFocus = &pendingRemoteFocus{remote: "dev", id: "new-1", deadline: now.Add(8 * time.Second)}
	home.lastRemoteFetch = now.Add(-remoteFocusRetryInterval)
	if !home.remoteFocusWantsFetch(now) {
		t.Fatal("a waiting request must ask for a fetch")
	}
	home.remotesFetchActive = true
	if home.remoteFocusWantsFetch(now) {
		t.Fatal("never while a fetch is in flight")
	}
	home.remotesFetchActive = false
	home.lastRemoteFetch = now.Add(-time.Second)
	if home.remoteFocusWantsFetch(now) {
		t.Fatal("a fetch 1 s ago is recent enough")
	}
	if !home.remoteFocusWantsFetch(now.Add(time.Second)) {
		t.Fatal("2 s after the last fetch it asks again")
	}
	if home.remoteFocusWantsFetch(now.Add(9 * time.Second)) {
		t.Fatal("no fetch after the request's TTL")
	}

	// Through a real tick: the fetch is started (the in-flight flag is set).
	home.lastRemoteFetch = now.Add(-remoteFocusRetryInterval)
	home.remoteSessionRefreshSec = 3600
	home.Update(tickMsg(now))
	if !home.remotesFetchActive {
		t.Fatal("the tick did not start a fetch for the waiting request")
	}
}
