package session

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestTitleFormatExplicitGroupConstructor(t *testing.T) {
	i := NewInstanceWithGroup("display", t.TempDir(), "chosen/nested")
	if got := i.GetTmuxSession().GroupPath; got != i.GroupPath {
		t.Fatalf("tmux group = %q, stored group = %q", got, i.GroupPath)
	}
}

func TestTitleFormatCommittedGroupLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	tmux.SetTitleFormat("{group}/{name}")
	t.Cleanup(func() { tmux.SetTitleFormat("") })
	socket := fmt.Sprintf("title-group-%d", time.Now().UnixNano())
	inst := NewInstanceWithGroupAndTool("display", t.TempDir(), "work/nested", "shell")
	sess := inst.GetTmuxSession()
	sess.SocketName = socket
	inst.TmuxSocketName = socket
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
	storage := newTestStorage(t)
	tree := NewGroupTree([]*Instance{inst})
	check := func(want string) {
		t.Helper()
		got := run("display-message", "-p", "-t", sess.Name, "#{E:set-titles-string}")
		if got != want+"/display" {
			t.Fatalf("rendered title=%q, want %q", got, want+"/display")
		}
	}
	save := func() {
		t.Helper()
		if err := storage.SaveWithGroups([]*Instance{inst}, tree); err != nil {
			t.Fatal(err)
		}
	}
	save()
	check("work/nested")
	run("set-option", "-t", sess.Name, "@agentdeck_group_path", "untouched-control")
	inst.Status = StatusWaiting
	save()
	check("untouched-control") // A status-only save must not publish title options.
	run("set-option", "-t", sess.Name, "@agentdeck_group_path", "work/nested")
	tree.CreateGroupPath("team/deep")
	tree.MoveSessionToGroup(inst, "team/deep")
	check("work/nested") // Uncommitted mutations must not change live metadata.
	save()
	check("team/deep")
	if err := tree.RenameGroup("team", "renamed"); err != nil {
		t.Fatal(err)
	}
	save()
	check("renamed/deep")
	tree.CreateGroupPath("destination")
	if err := tree.MoveGroupTo("renamed", "destination"); err != nil {
		t.Fatal(err)
	}
	save()
	check("destination/renamed/deep")

	// A newer commit may land while an earlier callback is publishing metadata.
	writes := 0
	_, err := reconcileGroupTitle(func() (*statedb.InstanceRow, error) { return storage.db.LoadInstanceByID(inst.ID) }, func(row *statedb.InstanceRow) error {
		writes++
		if writes == 1 {
			newer := *row
			newer.GroupPath = "concurrent/newest"
			if err := storage.db.UpsertInstances([]*statedb.InstanceRow{&newer}); err != nil {
				return err
			}
		}
		return sess.SetGroupTitleMetadata(row.GroupPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("reconcile writes=%d, want stale then latest", writes)
	}
	check("concurrent/newest")

	// A failed storage operation must leave terminal metadata at the committed value.
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	tree.MoveSessionToGroup(inst, "uncommitted")
	if err := storage.SaveWithGroups([]*Instance{inst}, tree); err == nil {
		t.Fatal("save to closed DB succeeded")
	}
	check("concurrent/newest")
}
