// Issue #1753 (black-screen follow-up to #1764) — on the reporter's live deck
// (~57 agent sessions, two TUIs), Ctrl+Q still showed ~1s of black screen before
// the list reappeared, reproducing when a large group was expanded and vanishing
// when it was collapsed.
//
// Two families of cause, both pinned here:
//
//  1. EMPTY first frame: View() returned "" whenever isAttaching was set, and
//     several attach flavors could reach the first post-resume View() with the
//     flag still set (remote exec commands cleared it only in the ExecCallback,
//     which Bubble Tea runs on its own goroutine after resuming the loop; the
//     double-click site pre-set the flag before a call that can return nil,
//     leaving it stuck forever). Fixes: every tea.ExecCommand clears the flag
//     via onExit inside Run() — i.e. while the loop is still parked — and
//     View() re-serves the last rendered frame instead of "" as the belt.
//
//  2. BLOCKED first frame: the per-row render path took Instance.mu (RLock) via
//     GetAutoName/GetAutoNameDescription while background UpdateStatus holds
//     that mutex as a WRITER across tmux subprocess calls (Exists probe 2s cap,
//     DetectTool capture 3s cap). Go's RWMutex queues new readers behind a
//     waiting writer, so the first frame stalled for seconds, scaling with the
//     number of visible rows — which is why a big expanded group reproduced it.
//     Additionally processStatusUpdate refreshed EVERY visible row per pass, an
//     unbudgeted burst of write-lock acquisitions when visible ≈ fleet. Fixes:
//     row labels come from the lock-free render snapshot, visible rows get a
//     budgeted round-robin (with a cheap activity-fingerprint skip), and group
//     expand becomes a trigger for that amortized path instead of a burst.
package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// blackscreenTestHome builds a Home that has rendered once, so
// lastRenderedFrame is populated the way it always is before any attach can be
// triggered in a real session (an attach needs a rendered list to select from).
func blackscreenTestHome(t *testing.T) *Home {
	t.Helper()
	inst := &session.Instance{ID: "s1", Title: "session-one", Tool: "shell", Status: session.StatusStopped}
	h := newTestHomeWithItems(200, 50, []session.Item{
		{Type: session.ItemTypeSession, Session: inst, Level: 0},
	})
	h.instances = []*session.Instance{inst}
	h.instanceByID = map[string]*session.Instance{inst.ID: inst}
	h.groupTree = session.NewGroupTree(h.instances)
	if v := h.View(); strings.TrimSpace(v) == "" {
		t.Fatal("precondition: initial View() should render a non-empty frame")
	}
	return h
}

// --- Family 1: the first View() after every attach flavor must be non-empty ---

// TestIssue1753_FirstViewNonEmptyAfterSessionAttach: the plain Enter-attach
// flavor (attachCmd). onExit must clear the flag before Run returns, and the
// first View() after that must render the list.
func TestIssue1753_FirstViewNonEmptyAfterSessionAttach(t *testing.T) {
	h := blackscreenTestHome(t)
	h.isAttaching.Store(true)
	cmd := attachCmd{
		session: &tmux.Session{Name: "agentdeck_issue1753_absent"},
		opts:    tmux.AttachOptions{DetachByte: 17},
		onExit:  func() { h.isAttaching.Store(false) },
	}
	_ = cmd.Run()
	if h.isAttaching.Load() {
		t.Fatal("attachCmd.Run returned with isAttaching still set (#1753)")
	}
	if strings.TrimSpace(h.View()) == "" {
		t.Fatal("first View() after session-attach return is empty: black screen (#1753)")
	}
}

// TestIssue1753_FirstViewNonEmptyAfterWindowAttach: the window-attach flavor.
// The #1764 review flagged that no test constructed attachWindowCmd at all, so
// its onExit wiring was unverified safety-critical code.
func TestIssue1753_FirstViewNonEmptyAfterWindowAttach(t *testing.T) {
	h := blackscreenTestHome(t)
	h.isAttaching.Store(true)
	cmd := attachWindowCmd{
		session:     &tmux.Session{Name: "agentdeck_issue1753_absent"},
		windowIndex: 1,
		detachByte:  17,
		onExit:      func() { h.isAttaching.Store(false) },
	}
	_ = cmd.Run()
	if h.isAttaching.Load() {
		t.Fatal("attachWindowCmd.Run returned with isAttaching still set: first frame after " +
			"a window detach renders blank (#1753)")
	}
	if strings.TrimSpace(h.View()) == "" {
		t.Fatal("first View() after window-attach return is empty: black screen (#1753)")
	}
}

// TestIssue1753_FirstViewNonEmptyAfterRemoteAttach: the remote SSH flavors.
// Before the fix neither remoteAttachCmd nor remoteCreateAndAttachCmd had an
// onExit at all — the flag was cleared only in the ExecCallback goroutine,
// which races the first View() after Bubble Tea resumes.
func TestIssue1753_FirstViewNonEmptyAfterRemoteAttach(t *testing.T) {
	// Host "" fails ValidateSSHHost immediately: Run errors without spawning
	// ssh, exactly the shape of a failed remote attach.
	runner := session.NewSSHRunner("issue1753", session.RemoteConfig{Host: ""})

	t.Run("attach", func(t *testing.T) {
		h := blackscreenTestHome(t)
		h.isAttaching.Store(true)
		cmd := remoteAttachCmd{
			runner:    runner,
			sessionID: "absent",
			onExit:    func() { h.isAttaching.Store(false) },
		}
		if err := cmd.Run(); err == nil {
			t.Fatal("precondition: remote attach against an empty host should fail")
		}
		if h.isAttaching.Load() {
			t.Fatal("remoteAttachCmd.Run returned with isAttaching still set (#1753)")
		}
		if strings.TrimSpace(h.View()) == "" {
			t.Fatal("first View() after remote-attach return is empty: black screen (#1753)")
		}
	})

	t.Run("create-and-attach", func(t *testing.T) {
		h := blackscreenTestHome(t)
		h.isAttaching.Store(true)
		cmd := remoteCreateAndAttachCmd{
			runner: runner,
			tool:   "shell",
			onExit: func() { h.isAttaching.Store(false) },
		}
		if err := cmd.Run(); err == nil {
			t.Fatal("precondition: remote create against an empty host should fail")
		}
		if h.isAttaching.Load() {
			t.Fatal("remoteCreateAndAttachCmd.Run returned with isAttaching still set (#1753)")
		}
		if strings.TrimSpace(h.View()) == "" {
			t.Fatal("first View() after remote create-and-attach return is empty (#1753)")
		}
	})
}

// TestIssue1753_FirstViewNonEmptyOnReloadingReturn: the auto-reload branch of
// the statusUpdateMsg handler (isReloading=true) returns early — before the fix
// no test ever set isReloading, so nothing pinned that this branch still leaves
// the first frame renderable.
func TestIssue1753_FirstViewNonEmptyOnReloadingReturn(t *testing.T) {
	h := blackscreenTestHome(t)
	h.reloadMu.Lock()
	h.isReloading = true
	h.reloadMu.Unlock()
	h.isAttaching.Store(true)

	_, _ = h.Update(statusUpdateMsg{})

	if h.isAttaching.Load() {
		t.Fatal("statusUpdateMsg (reloading branch) left isAttaching set (#1753)")
	}
	if strings.TrimSpace(h.View()) == "" {
		t.Fatal("first View() after reloading-branch attach return is empty (#1753)")
	}
}

// TestIssue1753_ViewFallsBackToLastFrameWhileAttaching is the belt itself:
// even when the flag is (wrongly) still set after the loop resumes, View()
// serves the last rendered frame instead of clearing the screen to black.
func TestIssue1753_ViewFallsBackToLastFrameWhileAttaching(t *testing.T) {
	h := blackscreenTestHome(t)
	want := h.View()

	h.isAttaching.Store(true)
	got := h.View()
	if strings.TrimSpace(got) == "" {
		t.Fatal("View() returned an empty frame while isAttaching was set: any path that " +
			"reaches the first post-resume View with the flag still set shows a black " +
			"screen (#1753)")
	}
	if got != want {
		t.Fatalf("View() while attaching should re-serve the last rendered frame, got a different frame")
	}
}

// TestIssue1753_AttachSessionNilReturnLeavesFlagClear pins the stuck-flag
// route: attachSession returns nil when the instance has no tmux session, and
// the flag must not be left set by anyone in that case. Before the fix the
// double-click handler pre-set isAttaching before calling attachSession, so a
// nil return suppressed View() forever.
func TestIssue1753_AttachSessionNilReturnLeavesFlagClear(t *testing.T) {
	h := blackscreenTestHome(t)
	inst := &session.Instance{ID: "no-tmux", Title: "no-tmux", Tool: "shell", Status: session.StatusStopped}

	if cmd := h.attachSession(inst); cmd != nil {
		t.Fatal("precondition: attachSession on an instance with no tmux session should return nil")
	}
	if h.isAttaching.Load() {
		t.Fatal("attachSession(nil-tmux) left isAttaching set: View() would be suppressed forever (#1753)")
	}

	// Source-level guard for the double-click site specifically: the block that
	// handles a double-click must not pre-set the flag.
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "if isDoubleClick {")
	if start < 0 {
		t.Fatal("double-click block not found in home.go")
	}
	block := text[start:]
	if end := strings.Index(block, "// Single click"); end >= 0 {
		block = block[:end]
	}
	if strings.Contains(block, "isAttaching.Store(true)") {
		t.Fatal("double-click handler pre-sets isAttaching before attachSession, which can " +
			"return nil and leave the flag stuck true — permanent black screen (#1753)")
	}
}

// TestIssue1753_AllExecCommandsClearFlagInRun is the source-level guard for the
// wiring: every tea.Exec call site in home.go that sets isAttaching must hand
// the clear to the command's onExit (run while the loop is parked), not only to
// the ExecCallback (run on its own goroutine after the loop resumed).
func TestIssue1753_AllExecCommandsClearFlagInRun(t *testing.T) {
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	text := string(src)

	for _, cmdType := range []string{
		"tea.Exec(attachCmd{",
		"tea.Exec(attachWindowCmd{",
		"tea.Exec(remoteAttachCmd{",
		"tea.Exec(remoteCreateAndAttachCmd{",
	} {
		idx := 0
		found := false
		for {
			at := strings.Index(text[idx:], cmdType)
			if at < 0 {
				break
			}
			found = true
			site := text[idx+at:]
			if end := strings.Index(site, "func(err error) tea.Msg"); end >= 0 {
				site = site[:end]
			}
			if !strings.Contains(site, "onExit:") {
				t.Errorf("%s call site without onExit: the attach flag would only be cleared "+
					"in the ExecCallback goroutine, racing the first post-resume View (#1753)", cmdType)
			}
			idx += at + len(cmdType)
		}
		if !found {
			t.Errorf("no %s call site found — update this guard if the command was renamed (#1753)", cmdType)
		}
	}

	// And each command type's Run must defer onExit.
	for _, runSig := range []string{
		"func (a attachCmd) Run() error {",
		"func (a attachWindowCmd) Run() error {",
		"func (r remoteAttachCmd) Run() error {",
		"func (r remoteCreateAndAttachCmd) Run() error {",
	} {
		body := funcBody(t, text, runSig)
		if !strings.Contains(body, "onExit") {
			t.Errorf("%s does not run onExit: the flag clear would not happen while the "+
				"loop is parked (#1753)", runSig)
		}
	}
}

// --- Family 2: the first frame must not block on or scale with visible rows ---

// TestIssue1753_RowLabelsRenderLockFree is the source-level guard for the
// blocked-first-frame fix: the overview row renderer must derive its labels
// from the render snapshot (sessionDisplayLabelsFromState), never from the
// Instance getters that take Instance.mu per row.
func TestIssue1753_RowLabelsRenderLockFree(t *testing.T) {
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	body := funcBody(t, string(src), "func (h *Home) renderSessionItem(")
	for _, banned := range []string{
		"sessionDisplayLabels(inst",
		"inst.GetAutoName()",
		"inst.GetAutoNameDescription()",
		"inst.GetStatusThreadSafe()",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("renderSessionItem calls %s: that takes Instance.mu per visible row, and a "+
				"background UpdateStatus holds that mutex as a writer across tmux subprocess "+
				"calls — the first post-detach frame then blocks for seconds, scaling with "+
				"visible row count (#1753). Read it from the render snapshot instead.", banned)
		}
	}

	// The snapshot must actually carry the label fields, or the lock-free path
	// would render blank titles.
	inst := &session.Instance{ID: "snap", Title: "handle", Tool: "shell", Status: session.StatusStopped}
	h := newTestHomeWithItems(120, 40, nil)
	h.instances = []*session.Instance{inst}
	h.refreshSessionRenderSnapshot(nil)
	state, ok := h.getSessionRenderSnapshot()["snap"]
	if !ok {
		t.Fatal("render snapshot missing instance after refresh")
	}
	if state.title != "handle" {
		t.Fatalf("render snapshot does not carry the row title (got %q): the lock-free label "+
			"path would render empty rows (#1753)", state.title)
	}
	if title, _ := sessionDisplayLabelsFromState(state); title != "handle" {
		t.Fatalf("sessionDisplayLabelsFromState = %q, want the instance handle", title)
	}
}

// TestIssue1753_VisibleRowRefreshIsBudgeted: with a large group expanded the
// visible set approaches fleet size, so "refresh every visible row per pass"
// degenerates into the very burst the off-screen batching exists to prevent —
// each UpdateStatus takes the Instance write lock, sometimes across tmux
// subprocess calls. Visible rows must be budgeted round-robin instead.
func TestIssue1753_VisibleRowRefreshIsBudgeted(t *testing.T) {
	const fleet = 30
	instances := make([]*session.Instance, 0, fleet)
	ids := make([]string, 0, fleet)
	for i := 0; i < fleet; i++ {
		// StatusRunning with no tmux session: UpdateStatus flips it to a
		// terminated status, so "status changed" counts UpdateStatus calls.
		inst := &session.Instance{
			ID:     fmt.Sprintf("v-%d", i),
			Title:  fmt.Sprintf("v-%d", i),
			Tool:   "shell",
			Status: session.StatusRunning,
		}
		instances = append(instances, inst)
		ids = append(ids, inst.ID)
	}
	h := newTestHomeWithItems(200, 50, nil)
	h.instances = instances

	req := statusUpdateRequest{viewOffset: 0, visibleHeight: fleet, flatItemIDs: ids}
	h.processStatusUpdate(req)

	changed := 0
	for _, inst := range instances {
		if inst.GetStatusThreadSafe() != session.StatusRunning {
			changed++
		}
	}
	// visibleStatusBatchSize(4) + the off-screen batch(2) is the absolute upper
	// bound of UpdateStatus calls in one pass; all 30 rows are visible here so
	// the off-screen batch has nothing to do.
	if changed > 6 {
		t.Fatalf("one processStatusUpdate pass refreshed %d of %d visible rows: the visible-row "+
			"burst is back — with a large group expanded this is an unbudgeted storm of "+
			"Instance write locks right when the user expects a frame (#1753)", changed, fleet)
	}
	if changed == 0 {
		t.Fatal("processStatusUpdate refreshed no visible rows at all: the budget must " +
			"amortize, not starve (#1753)")
	}

	// The round-robin must CYCLE: repeated passes eventually cover every row.
	// 12 passes x 4-row budget > 30 rows even with slack for skips.
	for pass := 0; pass < 12; pass++ {
		h.processStatusUpdate(req)
	}
	for _, inst := range instances {
		if inst.GetStatusThreadSafe() == session.StatusRunning {
			t.Fatalf("row %s never refreshed across repeated passes: the visible round-robin "+
				"does not cycle (#1753)", inst.ID)
		}
	}
}

// TestIssue1753_GroupExpandIsBudgetedTrigger: expanding a group must not run
// any synchronous per-row work on the event loop; it renders from the snapshot
// and nudges the budgeted status worker instead.
func TestIssue1753_GroupExpandIsBudgetedTrigger(t *testing.T) {
	const groupSize = 20
	instances := make([]*session.Instance, 0, groupSize)
	for i := 0; i < groupSize; i++ {
		instances = append(instances, &session.Instance{
			ID:        fmt.Sprintf("g-%d", i),
			Title:     fmt.Sprintf("g-%d", i),
			Tool:      "shell",
			Status:    session.StatusStopped,
			GroupPath: "biggroup",
		})
	}
	h := newTestHomeWithItems(200, 50, nil)
	h.instances = instances
	h.groupTree = session.NewGroupTree(instances)
	h.groupTree.CollapseGroup("biggroup")
	h.rebuildFlatItems()
	if len(h.flatItems) == 0 || h.flatItems[0].Type != session.ItemTypeGroup {
		t.Fatal("precondition: collapsed group row expected at the top of flatItems")
	}
	h.cursor = 0

	// Expand via the real key handler (tab toggles the group under the cursor).
	statusBefore := make(map[string]session.Status, groupSize)
	for _, inst := range instances {
		statusBefore[inst.ID] = inst.GetStatusThreadSafe()
	}
	_, _ = h.Update(tea.KeyMsg{Type: tea.KeyTab})

	// 1. No synchronous refresh burst: expanding changed no session status
	//    inline (rebuildFlatItems is pure in-memory).
	for _, inst := range instances {
		if inst.GetStatusThreadSafe() != statusBefore[inst.ID] {
			t.Fatalf("group expand refreshed session %s synchronously on the event loop (#1753)", inst.ID)
		}
	}

	// 2. The expand queued a budgeted refresh request for the worker.
	select {
	case req := <-h.statusTrigger:
		if len(req.flatItemIDs) == 0 {
			t.Fatal("group-expand trigger carried no session IDs (#1753)")
		}
	default:
		t.Fatal("group expand queued no status-update request: newly visible rows would " +
			"wait for the next tick instead of filling in amortized (#1753)")
	}

	// 3. Collapse must NOT nudge the worker (nothing became visible).
	_, _ = h.Update(tea.KeyMsg{Type: tea.KeyTab})
	select {
	case <-h.statusTrigger:
		t.Fatal("group collapse queued a status-update request: collapse reveals nothing (#1753)")
	default:
	}
}
