package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestTitleFormatWebAndTUIGroupPersistence(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	session.ConfigureTmuxDisplay(session.DisplaySettings{TitleFormat: "{group}/{name}"})
	t.Cleanup(func() { session.ConfigureTmuxDisplay(session.DisplaySettings{}) })
	h, storage := newHeadlessHomeForTest(t, "_test_title_format")
	inst := session.NewInstanceWithGroupAndTool("display", t.TempDir(), "before/nested", "shell")
	socket := fmt.Sprintf("title-web-%d", time.Now().UnixNano())
	inst.TmuxSocketName = socket
	sess := inst.GetTmuxSession()
	sess.SocketName = socket
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("new-session", "-d", "-s", sess.Name, "sleep 300")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", sess.Name).Run() })
	sess.ConfigureTerminalTitle()
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	if err := NewWebMutator(h).RenameGroup("before", "after"); err != nil {
		t.Fatal(err)
	}
	if got := run("display-message", "-p", "-t", sess.Name, "#{E:set-titles-string}"); got != "after/nested/display" {
		t.Fatalf("web rename rendered %q", got)
	}
	// The TUI move path changes its in-memory tree, then publishes via this save.
	h.groupTree.MoveSessionToGroup(h.getInstanceByID(inst.ID), "tui-target")
	h.forceSaveInstances()
	if got := run("display-message", "-p", "-t", sess.Name, "#{E:set-titles-string}"); got != "tui-target/display" {
		t.Fatalf("TUI save rendered %q", got)
	}
}
