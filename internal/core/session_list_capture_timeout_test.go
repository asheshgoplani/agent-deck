package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// slowBatchTmux is listBudgetTmux on a host too loaded to answer a batched
// capture in time (#3089): a `;`-joined capture-pane sequence hangs until the
// caller's timeout kills it, while a lone capture-pane answers after 300ms.
// exec replaces the shell so the timeout's kill reaches the sleeping process.
const slowBatchTmux = `#!/bin/sh
case " $* " in
*" capture-pane "*" ; "*) printf '%s\n' "$*" >> "$LIST_BUDGET/calls"; exec sleep 60;;
*" capture-pane "*) sleep 0.3;;
esac
` + "exec \"$LIST_BUDGET/budget-tmux\" \"$@\"\n"

// TestSessionListBatchCaptureTimeoutStaysBounded: when the batched capture
// times out, a live listing must not fall back to one capture-pane process
// per session, nor ask tmux per session because the slow batch outlasted the
// status caches. Before the fix 24 live sessions cost 24 more capture-pane
// processes and 144 has-session/list-panes/display-message processes after
// the batch's own timeout; now the pass ends right after the batch timeout
// with the skipped panes counted in the stats.
func TestSessionListBatchCaptureTimeoutStaysBounded(t *testing.T) {
	const n = 24
	dir := t.TempDir()
	t.Setenv("LIST_BUDGET", dir)
	profile := "_core_list_capture_timeout"
	instances := make([]*session.Instance, 0, n)
	var names strings.Builder
	for j := 0; j < n; j++ {
		inst := session.NewInstanceWithTool(fmt.Sprintf("slow-%d", j), t.TempDir(), "claude")
		inst.Status = session.StatusWaiting
		instances = append(instances, inst)
		fmt.Fprintln(&names, inst.GetTmuxSession().Name)
	}
	for name, body := range map[string]string{"names": names.String(), "budget-tmux": listBudgetTmux, "tmux": slowBatchTmux} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	seedStore(t, profile, nil, instances...)

	started := time.Now()
	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
	elapsed := time.Since(started)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(out.Sessions) != n {
		t.Fatalf("listed %d sessions, want %d", len(out.Sessions), n)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	captureProcs := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, "capture-pane") {
			captureProcs++
		}
	}
	t.Logf("%d sessions: %d capture-pane processes, %v, stats %+v", n, captureProcs, elapsed, out.Stats)
	if captureProcs > 1 {
		t.Errorf("a timed-out batch was followed by %d more capture-pane processes, want none", captureProcs-1)
	}
	if out.Stats == nil || out.Stats.CapturesSkipped != n {
		t.Errorf("stats = %+v, want captures_skipped %d", out.Stats, n)
	}
	// A quiet pass costs 4 tmux processes (see
	// TestSessionListLiveTmuxCallsDoNotScaleWithSessions); the timed-out
	// batch and one re-warm of the caches it outlasted may add a few, but
	// nothing that grows with the number of sessions.
	if out.Stats != nil && out.Stats.TmuxCalls > 10 {
		t.Errorf("tmux_calls = %d for %d sessions after a batch timeout, want a bounded handful", out.Stats.TmuxCalls, n)
	}
	// The rows say their pane was not read in this pass.
	for _, row := range out.Sessions {
		if row.StatusSource != "stale" {
			t.Errorf("%s: status_source = %q, want stale (pane capture skipped)", row.Title, row.StatusSource)
		}
	}
	// Batch timeout is 3s + 50ms per pane; the fallback added 300ms per pane.
	budget := 3*time.Second + n*50*time.Millisecond + 3*time.Second
	if elapsed > budget {
		t.Errorf("listing took %v after a batch timeout, budget %v", elapsed, budget)
	}
}

// TestSessionListExcludeArchived: `list --json --no-archived` lists only the
// unarchived rows and runs the status pass for them alone, while a live
// child still recovers its parent path from an archived parent row.
func TestSessionListExcludeArchived(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LIST_BUDGET", dir)
	profile := "_core_list_exclude_archived"
	parent := session.NewInstanceWithTool("archived-parent", t.TempDir(), "claude")
	parent.Status = session.StatusStopped
	parent.ArchivedAt = time.Now().Add(-time.Hour)
	other := session.NewInstanceWithTool("archived-other", t.TempDir(), "claude")
	other.Status = session.StatusWaiting
	other.ArchivedAt = time.Now().Add(-time.Hour)
	child := session.NewInstanceWithTool("live-child", t.TempDir(), "claude")
	child.Status = session.StatusWaiting
	child.ParentSessionID = parent.ID
	live := session.NewInstanceWithTool("live", t.TempDir(), "claude")
	live.Status = session.StatusWaiting
	var names strings.Builder
	for _, inst := range []*session.Instance{parent, other, child, live} {
		fmt.Fprintln(&names, inst.GetTmuxSession().Name)
	}
	for name, body := range map[string]string{"names": names.String(), "tmux": listBudgetTmux} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	seedStore(t, profile, nil, parent, other, child, live)

	for _, liveStatus := range []bool{true, false} {
		out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList,
			SessionListIn{Profile: profile, LiveStatus: liveStatus, ExcludeArchived: true})
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got := map[string]SessionRow{}
		for _, row := range out.Sessions {
			got[row.Title] = row
		}
		if len(got) != 2 || got["live"].ID == "" || got["live-child"].ID == "" {
			t.Fatalf("live_status=%v: listed %v, want only live and live-child", liveStatus, out.Sessions)
		}
		if got["live-child"].ParentProjectPath != parent.ProjectPath {
			t.Errorf("live_status=%v: child parent_project_path = %q, want archived parent's %q", liveStatus, got["live-child"].ParentProjectPath, parent.ProjectPath)
		}
		if liveStatus && (out.Stats == nil || out.Stats.Sessions != 2) {
			t.Errorf("status pass stats = %+v, want 2 sessions (archived rows skipped)", out.Stats)
		}
	}

	all, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(all.Sessions) != 4 {
		t.Fatalf("default listing has %d rows, want all 4", len(all.Sessions))
	}
}
