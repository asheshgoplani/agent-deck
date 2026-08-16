// Issue #1753 — pressing Ctrl+Q inside an attached session took noticeably long
// to come back to the list on a ~70-session deck, and so did switching sessions.
//
// Measured in a sandbox (isolated HOME/profile/tmux socket, 70 synthetic
// `--cmd sleep` sessions, 220x50, 10 attach->Ctrl+Q cycles), instrumented at every
// stage between the detach byte and the first repaint:
//
//	stage                                    before      after
//	GetWorkDir (2 tmux subprocess spawns)    5-13 ms     not called
//	RefreshSessionCache (list-windows -a)    1-2 ms      off the event loop
//	RefreshPaneInfoCache (list-panes -a)     2-4 ms      off the event loop
//	forced UpdateStatus (capture-pane)       included    off the event loop
//	detach key -> first repaint (median)     11.0 ms     6.7 ms   (idle server)
//	detach key -> first repaint (median)     17.1 ms     6.1 ms   (busy server)
//
// The absolute numbers are small in a sandbox of idle `sleep` panes; the point of
// the fix is that NONE of that work is on the event loop any more, so the delay no
// longer scales with fleet size or tmux-server load — on the reporter's deck of ~70
// live agent sessions each of those round-trips costs an order of magnitude more.
//
// What these tests pin:
//
//  1. the attach-return message handlers hand the tmux/disk reconciliation to a
//     tea.Cmd instead of running it inline (fails on the pre-fix code, which
//     populated the render snapshot before Update returned),
//  2. the reconciliation still HAPPENS — the deferred Cmd does the work and asks
//     for a repaint, so no row can go stale forever,
//  3. attachCmd clears the attach flag before Bubble Tea resumes, so the first
//     post-detach View renders the list instead of the "" that View returns while
//     isAttaching is set,
//  4. a generous wall-clock budget on the handler at 70 sessions, to catch a future
//     re-introduction of any O(fleet) blocking call on the event loop.

package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// homeWithInstances builds a Home whose instance index and flat item list both
// describe n sessions, which is what the attach-return path walks.
func homeWithInstances(t *testing.T, n int) (*Home, []*session.Instance) {
	t.Helper()

	instances := make([]*session.Instance, 0, n)
	items := make([]session.Item, 0, n)
	byID := make(map[string]*session.Instance, n)
	for i := 0; i < n; i++ {
		inst := &session.Instance{
			ID:    fmt.Sprintf("sess-%d", i),
			Title: fmt.Sprintf("sess-%d", i),
			Tool:  "shell",
			// Stopped keeps the reconciliation a pure in-memory no-op: with no tmux
			// session UpdateStatus leaves a stopped instance stopped, so nothing
			// publishes web state or writes to SQLite and the only observable effect
			// is the render snapshot these tests key on.
			Status: session.StatusStopped,
		}
		instances = append(instances, inst)
		byID[inst.ID] = inst
		items = append(items, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 0})
	}

	h := newTestHomeWithItems(200, 50, items)
	h.instances = instances
	h.instanceByID = byID
	// The attach-return handlers re-derive rows from the group tree, so it has to
	// describe the same fleet or the rebuild is vacuous.
	h.groupTree = session.NewGroupTree(instances)
	return h, instances
}

// yieldsMsg executes cmd (walking tea.Batch trees) and reports whether any
// resulting message has the given concrete type name.
func yieldsMsg(cmd tea.Cmd, typeName string) bool {
	if cmd == nil {
		return false
	}
	return msgYields(cmd(), typeName)
}

func msgYields(msg tea.Msg, typeName string) bool {
	if msg == nil {
		return false
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if yieldsMsg(c, typeName) {
				return true
			}
		}
		return false
	}
	return fmt.Sprintf("%T", msg) == typeName
}

// TestIssue1753_StatusUpdateDefersReconcileOffTheEventLoop is the core regression
// gate. refreshAttachedSessionStatus ends by publishing a fresh render snapshot, so
// "snapshot still empty when Update returned" is a precise proxy for "the tmux/disk
// reconciliation did not run on the event loop". Pre-fix the snapshot was populated
// inline and this fails; post-fix it is published by the returned Cmd.
func TestIssue1753_StatusUpdateDefersReconcileOffTheEventLoop(t *testing.T) {
	h, instances := homeWithInstances(t, 8)
	attached := instances[0]

	if snap := h.getSessionRenderSnapshot(); len(snap) != 0 {
		t.Fatalf("precondition: render snapshot should start empty, has %d entries", len(snap))
	}

	_, cmd := h.Update(statusUpdateMsg{attachedSessionID: attached.ID})

	if snap := h.getSessionRenderSnapshot(); len(snap) != 0 {
		t.Fatalf("attach-return reconciliation ran inline on the event loop: "+
			"render snapshot already has %d entries when Update returned (#1753)", len(snap))
	}
	if !yieldsMsg(cmd, "ui.attachReturnSyncedMsg") {
		t.Fatal("attach-return Update returned no async reconcile Cmd: the deferred " +
			"work would never run, leaving rows stale (#1753)")
	}
	// ...and the deferred Cmd must actually do the reconciliation.
	if snap := h.getSessionRenderSnapshot(); len(snap) == 0 {
		t.Fatal("async reconcile Cmd ran but published no render snapshot (#1753)")
	}
}

// TestIssue1753_SwitcherReturnDefersReconcileOffTheEventLoop covers the "switching
// sessions is laggy too" half of the report: the in-attach switcher and scrollback
// return paths ran the same inline reconciliation.
func TestIssue1753_SwitcherReturnDefersReconcileOffTheEventLoop(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  func(id string) tea.Msg
	}{
		{"switcher", func(id string) tea.Msg { return openSwitcherMsg{fromSessionID: id} }},
		{"scrollback", func(id string) tea.Msg { return openScrollbackMsg{fromSessionID: id} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, instances := homeWithInstances(t, 8)
			from := instances[0]

			_, cmd := h.Update(tc.msg(from.ID))

			if snap := h.getSessionRenderSnapshot(); len(snap) != 0 {
				t.Fatalf("%s return reconciled inline on the event loop: snapshot has %d entries (#1753)",
					tc.name, len(snap))
			}
			if !yieldsMsg(cmd, "ui.attachReturnSyncedMsg") {
				t.Fatalf("%s return returned no async reconcile Cmd (#1753)", tc.name)
			}
		})
	}
}

// TestIssue1753_DelayedRefreshDefersTmuxWork pins the second stall: the delayed
// post-attach catch-up (attachReturnRefreshMsg, ~350ms after the list came back)
// refreshed both tmux caches inline.
func TestIssue1753_DelayedRefreshDefersTmuxWork(t *testing.T) {
	h, _ := homeWithInstances(t, 8)

	_, cmd := h.Update(attachReturnRefreshMsg{})

	if snap := h.getSessionRenderSnapshot(); len(snap) != 0 {
		t.Fatalf("delayed attach-return refresh did tmux work on the event loop: "+
			"snapshot has %d entries when Update returned (#1753)", len(snap))
	}
	if !yieldsMsg(cmd, "ui.attachReturnSyncedMsg") {
		t.Fatal("delayed attach-return refresh returned no async Cmd (#1753)")
	}
	if snap := h.getSessionRenderSnapshot(); len(snap) == 0 {
		t.Fatal("delayed refresh Cmd ran but published no render snapshot (#1753)")
	}
}

// TestIssue1753_SyncedMsgRebuildsRowsWithoutTmux asserts the repaint half of the
// split is cheap: handling attachReturnSyncedMsg only re-derives rows in memory.
func TestIssue1753_SyncedMsgRebuildsRowsWithoutTmux(t *testing.T) {
	h, _ := homeWithInstances(t, 70)

	start := time.Now()
	_, cmd := h.Update(attachReturnSyncedMsg{})
	elapsed := time.Since(start)

	if cmd != nil {
		t.Fatalf("attachReturnSyncedMsg should not schedule further work, got %T", cmd)
	}
	// Pure in-memory rebuild of 70 rows. The budget is deliberately loose for slow
	// CI runners; it only fails if a tmux/disk call sneaks back in.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("attachReturnSyncedMsg took %s for 70 sessions: something blocking "+
			"came back into the repaint path (#1753)", elapsed)
	}
}

// TestIssue1753_RemoteSessionsUnaffected covers the RemoteSession side of the
// attach-return paths this PR touches.
//
//   - Remote attach (attachRemoteSession) returns statusUpdateMsg with an EMPTY
//     attachedSessionID, so it schedules no reconcile — exactly as the inline
//     refreshAttachedSessionStatus("") it replaced returned immediately. Pinning that
//     keeps a future edit from firing a local-tmux reconcile for a remote session.
//   - The extra rebuild introduced by attachReturnSyncedMsg must keep a selected
//     remote row selected, since remote items are appended to flatItems from
//     h.remoteSessions on every rebuild.
func TestIssue1753_RemoteSessionsUnaffected(t *testing.T) {
	h, _ := homeWithInstances(t, 4)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "r1", Title: "remote-one", Tool: "shell", Status: "running"},
			{ID: "r2", Title: "remote-two", Tool: "shell", Status: "running"},
		},
	}
	h.rebuildFlatItems()

	target := -1
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteSession.ID == "r2" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("precondition: remote session row r2 not present in flatItems")
	}
	h.cursor = target

	// A remote attach return carries no session ID: no local reconcile may be scheduled.
	if _, cmd := h.Update(statusUpdateMsg{}); yieldsMsg(cmd, "ui.attachReturnSyncedMsg") {
		t.Error("remote attach return scheduled a local-tmux reconcile; statusUpdateMsg with " +
			"no attachedSessionID must schedule none (#1753)")
	}

	// Re-select r2 (the handler above rebuilt the list) and check the new
	// attachReturnSyncedMsg rebuild preserves a remote selection.
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteSession.ID == "r2" {
			h.cursor = i
			break
		}
	}
	_, _ = h.Update(attachReturnSyncedMsg{})

	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor %d out of range after rebuild (%d items)", h.cursor, len(h.flatItems))
	}
	got := h.flatItems[h.cursor]
	if got.Type != session.ItemTypeRemoteSession || got.RemoteSession == nil || got.RemoteSession.ID != "r2" {
		t.Fatalf("attachReturnSyncedMsg rebuild lost the remote selection: cursor now on %v (#1753)", got.Type)
	}
}

// TestIssue1753_AttachReturnUpdateStaysUnderBudget is the wall-clock budget guard
// for the handler itself at the reporter's fleet size. Generous on purpose: this is
// a floor against re-introducing an O(fleet) blocking call, not a benchmark.
func TestIssue1753_AttachReturnUpdateStaysUnderBudget(t *testing.T) {
	h, instances := homeWithInstances(t, 70)

	start := time.Now()
	_, _ = h.Update(statusUpdateMsg{attachedSessionID: instances[0].ID})
	elapsed := time.Since(start)

	const budget = 150 * time.Millisecond
	if elapsed > budget {
		t.Fatalf("attach-return Update took %s at 70 sessions (budget %s): the detach "+
			"path is blocking the event loop again (#1753)", elapsed, budget)
	}
}

// TestIssue1753_AttachCmdClearsAttachFlagBeforeReturning pins the ordering that makes
// the first post-detach repaint deterministic. View() returns "" while isAttaching is
// set; clearing it only in the ExecCallback (which Bubble Tea runs on its own
// goroutine, after it has already resumed the loop) raced that first View, and losing
// the race left the screen blank until the next message arrived.
func TestIssue1753_AttachCmdClearsAttachFlagBeforeReturning(t *testing.T) {
	h := NewHome()
	h.isAttaching.Store(true)

	// A session name that cannot exist: AttachWithOptions fails its Exists() probe
	// and returns immediately, without attaching anything.
	cmd := attachCmd{
		session: &tmux.Session{Name: "agentdeck_issue1753_absent_session"},
		opts:    tmux.AttachOptions{DetachByte: 17},
		onExit:  func() { h.isAttaching.Store(false) },
	}
	_ = cmd.Run()

	if h.isAttaching.Load() {
		t.Fatal("attachCmd.Run returned with isAttaching still set: View() will render " +
			"\"\" on the first frame after Bubble Tea resumes, so the list comes back " +
			"blank until the next message (#1753)")
	}
}

func TestIssue1753_AttachWindowCmdClearsAttachFlagBeforeReturning(t *testing.T) {
	h := NewHome()
	h.isAttaching.Store(true)

	// A session name that cannot exist makes AttachWindow return after its
	// existence probe, without selecting or attaching to a tmux window.
	cmd := attachWindowCmd{
		session:     &tmux.Session{Name: "agentdeck_issue1753_absent_window_session"},
		windowIndex: 1,
		detachByte:  17,
		onExit:      func() { h.isAttaching.Store(false) },
	}
	_ = cmd.Run()

	if h.isAttaching.Load() {
		t.Fatal("attachWindowCmd.Run returned with isAttaching still set: View() will render " +
			"\"\" on the first frame after Bubble Tea resumes")
	}
}

// TestIssue1753_AttachReturnHandlersHaveNoInlineTmuxCalls is the source-level guard.
// The behavioural tests above can only observe the snapshot; this one states the rule
// directly, so a future edit that re-adds an inline tmux round-trip to any of the four
// attach-return handlers fails here with the reason attached.
func TestIssue1753_AttachReturnHandlersHaveNoInlineTmuxCalls(t *testing.T) {
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	text := string(src)

	banned := []string{
		"tmux.RefreshSessionCache()",
		"tmux.RefreshPaneInfoCache()",
		"h.refreshAttachedSessionStatus(",
		".GetWorkDir()",
	}
	// followAttachReturnCwd is called inline by those handlers, so a blocking call
	// added inside it would restore the latency while every per-handler ban above
	// still passed. It must take the pane CWD as a parameter and consult the setting,
	// never probe tmux itself.
	cwdHelper := funcBody(t, text, "func (h *Home) followAttachReturnCwd(")
	if strings.Contains(cwdHelper, "GetWorkDir()") {
		t.Error("followAttachReturnCwd probes tmux itself: it runs inline on the event loop " +
			"from the attach-return handlers, so that puts a subprocess spawn back between " +
			"the detach key and the first repaint (#1753)")
	}
	if !strings.Contains(cwdHelper, "GetFollowCwdOnAttach()") {
		t.Error("followAttachReturnCwd no longer checks GetFollowCwdOnAttach() before doing " +
			"work: the attach-return path pays for a feature that defaults to off (#1753)")
	}
	for _, handler := range []string{
		"case statusUpdateMsg:",
		"case openSwitcherMsg:",
		"case openScrollbackMsg:",
		"case attachReturnRefreshMsg:",
		"case attachReturnSyncedMsg:",
	} {
		block := handlerBlock(t, text, handler)
		for _, call := range banned {
			if strings.Contains(block, call) {
				t.Errorf("%s runs %s inline on the Bubble Tea event loop — that blocks the "+
					"first repaint after detach. Hand it to attachReturnSyncCmd / "+
					"attachReturnRefreshCmd instead (#1753)", handler, call)
			}
		}
	}

	// And the deferred path must still exist: an "optimisation" that just deletes the
	// reconciliation would pass the bans above while leaving rows stale forever.
	for _, required := range []string{
		"func (h *Home) attachReturnSyncCmd(",
		"func (h *Home) attachReturnRefreshCmd(",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("missing %s: the attach-return reconciliation has no deferred home (#1753)", required)
		}
	}

	// Every local attach path must wire onExit, or the first repaint goes back to
	// racing the ExecCallback goroutine.
	mainKeyBody := funcBody(t, text, "func (h *Home) handleMainKey(")
	attachSites := []struct {
		name string
		body string
		cmd  string
	}{
		{
			name: "session",
			body: braceBlock(t, funcBody(t, text, "func (h *Home) attachSession("), "attachCmd{"),
			cmd:  "attachCmd{",
		},
		{
			name: "window",
			body: braceBlock(t, handlerBlock(t, mainKeyBody, `case "enter":`), "attachWindowCmd{"),
			cmd:  "attachWindowCmd{",
		},
		{
			name: "sandbox terminal",
			body: braceBlock(t, handlerBlock(t, mainKeyBody, `case "E":`), "attachCmd{"),
			cmd:  "attachCmd{",
		},
	}
	for _, site := range attachSites {
		if !strings.Contains(site.body, site.cmd) ||
			!strings.Contains(site.body, "onExit:") ||
			!strings.Contains(site.body, "isAttaching.Store(false)") {
			t.Errorf("%s attach no longer clears isAttaching via onExit: the first "+
				"post-detach View can race the ExecCallback goroutine and render blank (#1753)",
				site.name)
		}
	}

	// The pane-CWD probe (two tmux subprocess spawns) must stay behind the
	// follow-cwd setting instead of running on every detach.
	attachSessionBody := funcBody(t, text, "func (h *Home) attachSession(")
	workDirHelper := braceBlock(t, attachSessionBody, "workDirIfFollowing := func(")
	followCwdGate := regexp.MustCompile(
		`if\s+!followCwd\s*\|\|\s*ts\s*==\s*nil\s*\{\s*return\s+""\s*\}`,
	)
	if !followCwdGate.MatchString(workDirHelper) ||
		!strings.Contains(workDirHelper, "GetWorkDir()") {
		t.Error("attachSession no longer returns before GetWorkDir when follow-CWD is disabled " +
			"or the tmux session is nil (#1753)")
	}
}

// funcBody returns the source text of one top-level function, from its signature to
// the start of the next one.
func funcBody(t *testing.T, text, signature string) string {
	t.Helper()
	start := strings.Index(text, signature)
	if start < 0 {
		t.Fatalf("function %q not found in home.go", signature)
	}
	rest := text[start+len(signature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// braceBlock returns the brace-delimited block following marker.
func braceBlock(t *testing.T, text, marker string) string {
	t.Helper()
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("block marker %q not found in home.go", marker)
	}
	openOffset := strings.Index(text[start:], "{")
	if openOffset < 0 {
		t.Fatalf("block marker %q has no opening brace in home.go", marker)
	}
	open := start + openOffset
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	t.Fatalf("block marker %q has no closing brace in home.go", marker)
	return ""
}

// handlerBlock returns the source text of one `case <label>` arm: from the label up to
// the next `\n\tcase ` at the same indentation, which is where the next arm starts.
func handlerBlock(t *testing.T, text, label string) string {
	t.Helper()
	start := strings.Index(text, label)
	if start < 0 {
		t.Fatalf("handler %q not found in home.go", label)
	}
	rest := text[start+len(label):]
	if end := strings.Index(rest, "\n\tcase "); end >= 0 {
		return rest[:end]
	}
	return rest
}
