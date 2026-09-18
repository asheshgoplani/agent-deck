package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// armWindowCloseSession creates a real isolated tmux session with two named
// windows and wraps it in a session.Instance, so closeSessionWindow (the CLI
// parity path for the TUI's window-row 'd') can be exercised end to end
// without a full agent-deck registry/profile setup. Returns the instance,
// socket, session name and the extra window's stable id.
func armWindowCloseSession(t *testing.T) (*session.Instance, string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}

	socket := fmt.Sprintf("wcc%d", os.Getpid())
	target := "agentdeck_windowclose_cli"
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-x", "80", "-y", "24", "-s", target, "-n", "agent", "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	out, err := exec.Command("tmux", "-L", socket, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", target, "-n", "shell", "sleep", "300").CombinedOutput()
	if err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	extraID := strings.TrimSpace(string(out))

	inst := &session.Instance{ID: "cli-wc-1", Title: "cli-wc", Status: session.StatusRunning}
	inst.SetTmuxSessionForTest(&tmux.Session{Name: target, SocketName: socket})
	return inst, socket, target, extraID
}

// windowIDsViaCLI lists the live window ids of target.
func windowIDsViaCLI(t *testing.T, socket, target string) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_id}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func windowCountViaCLI(t *testing.T, socket, target string) int {
	t.Helper()
	return len(windowIDsViaCLI(t, socket, target))
}

// TestCloseSessionWindow_RequiresYes — like the other destructive verbs in
// the tree (session cleanup, inbox purge, ownership reconcile), nothing is
// killed without --yes; the resolved window is returned so the caller can
// print what --yes would close.
func TestCloseSessionWindow_RequiresYes(t *testing.T) {
	inst, socket, target, extraID := armWindowCloseSession(t)

	win, err := closeSessionWindow(inst, "1", false)
	if !errors.Is(err, errSessionWindowConfirmRequired) {
		t.Fatalf("closeSessionWindow without --yes: err = %v, want errSessionWindowConfirmRequired", err)
	}
	if win.ID != extraID || win.Index != 1 || win.Name != "shell" {
		t.Fatalf("unconfirmed close must still resolve the window it would close, got %+v (want id %s, index 1, name shell)", win, extraID)
	}
	if got := windowCountViaCLI(t, socket, target); got != 2 {
		t.Fatalf("window count after unconfirmed close = %d, want 2 (nothing may be killed)", got)
	}
}

// TestCloseSessionWindow_KillsExtraWindow proves `agent-deck session window
// close --yes` (via closeSessionWindow) kills exactly the addressed window,
// leaving the others intact, and reports the window by id, index and name.
func TestCloseSessionWindow_KillsExtraWindow(t *testing.T) {
	inst, socket, target, extraID := armWindowCloseSession(t)

	win, err := closeSessionWindow(inst, "1", true)
	if err != nil {
		t.Fatalf("closeSessionWindow: %v", err)
	}
	if win.ID != extraID || win.Index != 1 || win.Name != "shell" {
		t.Fatalf("closed window = %+v, want id %s, index 1, name shell", win, extraID)
	}
	ids := windowIDsViaCLI(t, socket, target)
	if len(ids) != 1 || ids[0] == extraID {
		t.Fatalf("windows after close = %v, want exactly one window and not %s", ids, extraID)
	}
}

// TestCloseSessionWindow_AcceptsWindowID — the window may be addressed by
// its stable tmux id (@N) as well as by index, so a caller that read the id
// from --json (or from the TUI dialog) can close that exact window even if
// tmux renumbered since.
func TestCloseSessionWindow_AcceptsWindowID(t *testing.T) {
	inst, socket, target, extraID := armWindowCloseSession(t)

	win, err := closeSessionWindow(inst, extraID, true)
	if err != nil {
		t.Fatalf("closeSessionWindow(%s): %v", extraID, err)
	}
	if win.ID != extraID || win.Name != "shell" {
		t.Fatalf("closed window = %+v, want id %s name shell", win, extraID)
	}
	if ids := windowIDsViaCLI(t, socket, target); len(ids) != 1 || ids[0] == extraID {
		t.Fatalf("windows after close = %v, want exactly one window and not %s", ids, extraID)
	}
}

// TestCloseSessionWindow_RefusesLastWindow proves the CLI path refuses to
// kill a session's only window, same as the TUI guard.
func TestCloseSessionWindow_RefusesLastWindow(t *testing.T) {
	inst, socket, target, extraID := armWindowCloseSession(t)
	// Kill the extra window first so only one remains.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":"+extraID).CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if got := windowCountViaCLI(t, socket, target); got != 1 {
		t.Fatalf("setup: window count = %d, want 1", got)
	}

	win, err := closeSessionWindow(inst, "0", true)
	if !errors.Is(err, tmux.ErrLastWindow) {
		t.Fatalf("closeSessionWindow on the last window: err = %v, want ErrLastWindow", err)
	}
	if win.Name != "agent" {
		t.Fatalf("refusal must still name the window it refused, got %+v", win)
	}
	if got := windowCountViaCLI(t, socket, target); got != 1 {
		t.Fatalf("window count after refused close = %d, want 1", got)
	}
}

// TestCloseSessionWindow_UnknownWindowIsNotFound proves an out-of-range
// index, an unknown id and a non-numeric reference all report
// session-window-not-found rather than a bare tmux error.
func TestCloseSessionWindow_UnknownWindowIsNotFound(t *testing.T) {
	inst, socket, target, _ := armWindowCloseSession(t)
	for _, ref := range []string{"99", "@999999", "shell"} {
		if _, err := closeSessionWindow(inst, ref, true); !errors.Is(err, errSessionWindowNotFound) {
			t.Errorf("closeSessionWindow(%q): err = %v, want errSessionWindowNotFound", ref, err)
		}
	}
	if got := windowCountViaCLI(t, socket, target); got != 2 {
		t.Fatalf("window count after not-found closes = %d, want 2", got)
	}
}

// TestSessionWindowPayload pins the --json shape of `session window close`:
// {"session","window_id","window_index","window_name","closed"} on success,
// with no "reason" key.
func TestSessionWindowPayload(t *testing.T) {
	inst := &session.Instance{ID: "sess-1", Title: "cli-wc"}
	raw, err := json.Marshal(sessionWindowPayload(inst, tmux.WindowInfo{Index: 1, ID: "@7", Name: "shell"}, true, ""))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"session":      "sess-1",
		"window_id":    "@7",
		"window_index": float64(1),
		"window_name":  "shell",
		"closed":       true,
	}
	if len(got) != len(want) {
		t.Fatalf("payload keys = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// TestSessionWindowPayload_RefusalIncludesReason pins the --json shape of a
// refused close: closed:false plus a "reason" key naming why.
func TestSessionWindowPayload_RefusalIncludesReason(t *testing.T) {
	inst := &session.Instance{ID: "sess-1", Title: "cli-wc"}
	raw, err := json.Marshal(sessionWindowPayload(inst, tmux.WindowInfo{Index: 1, ID: "@7", Name: "shell"}, false, "last_window"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"session":      "sess-1",
		"window_id":    "@7",
		"window_index": float64(1),
		"window_name":  "shell",
		"closed":       false,
		"reason":       "last_window",
	}
	if len(got) != len(want) {
		t.Fatalf("payload keys = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// TestSessionWindowHelp_DocumentsFlagsAndAlias — the help text must tell a
// caller about --yes, the @id form and the kill alias.
func TestSessionWindowHelp_DocumentsFlagsAndAlias(t *testing.T) {
	out := captureStdout(t, printSessionWindowHelp)
	for _, want := range []string{"--yes", "--force", "@12", "kill", "Alias for close", "--json", "window_id", "last remaining window"} {
		if !strings.Contains(out, want) {
			t.Errorf("session window help is missing %q\n--- got ---\n%s", want, out)
		}
	}
	usage := captureStdout(t, printSessionWindowCloseUsage)
	if !strings.Contains(usage, "--yes") || !strings.Contains(usage, "@window-id") {
		t.Errorf("session window close usage must mention --yes and @window-id\n--- got ---\n%s", usage)
	}
}
