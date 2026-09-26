package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Review of fix/guard-error-hidden round 1, 2026-09-23: treating "no default
// server socket in this process's view" as proof of absence wrote error for
// HEALTHY sessions. From inside a process, "no default server exists" and "the
// default server lives at a path I do not compute" look the same, so a
// foreign-server TUI that cannot list the default server must form no verdict:
// the child's row stays running and its parent gets no event.
//
// The child lives on the test's isolated DEFAULT server
// (<TMUX_TMPDIR>/tmux-<uid>/default). The TUI runs inside a private -S server,
// and each case changes only what the TUI can see of the default server.

func TestForeignTmuxServer_UnlistableDefaultServerWritesNothingForHealthySession(t *testing.T) {
	skipIfNoTmuxBinary(t)
	bin := foreignTUIBinary(t)
	realTmpdir := os.Getenv("TMUX_TMPDIR")
	defaultSock := filepath.Join(realTmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	for n, c := range []struct {
		name string
		// setup returns the TUI's TMUX_TMPDIR after any change to its world.
		setup func(t *testing.T) string
	}{
		// The TUI's TMUX_TMPDIR differs from the fleet's (an agent's private
		// server started with its own TMUX_TMPDIR, an ssh/launchd env, a
		// PrivateTmp namespace): its computed default socket does not exist,
		// while the real default server is healthy elsewhere.
		{"TMUX_TMPDIR mismatch", func(t *testing.T) string {
			// Short /tmp root, not t.TempDir(): socket paths must fit sun_path.
			d, err := os.MkdirTemp("/tmp", "adfs-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(d) })
			return d
		}},
		// The default socket file was unlinked while the server lives (the
		// macOS periodic /tmp cleaner, systemd-tmpfiles).
		{"default socket unlinked, server alive", func(t *testing.T) string {
			if err := os.Rename(defaultSock, defaultSock+".moved"); err != nil {
				t.Fatalf("move socket: %v", err)
			}
			t.Cleanup(func() { _ = os.Rename(defaultSock+".moved", defaultSock) })
			return realTmpdir
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			profile := fmt.Sprintf("_test_foreign_healthy_%d", n)
			d, storage := bootstrapDaemonProfile(t, profile)
			ResetInboxFingerprintCacheForTest()
			t.Cleanup(ResetInboxFingerprintCacheForTest)

			child := startFramePane(t, "claude", "macapp-ux-audit", piCorpusFrame(t, uxAuditFrame))
			parentID := fmt.Sprintf("foreign-healthy-parent-%d", n)
			child.ParentSessionID = parentID
			child.Status = StatusRunning
			parent := &Instance{ID: parentID, Title: "conductor", ProjectPath: "/tmp/" + parentID, GroupPath: DefaultGroupPath,
				Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatal(err)
			}
			db := storage.GetDB()
			if err := db.RegisterInstance(false); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{child.ID, parentID} {
				if err := db.WriteStatus(id, "running", "claude"); err != nil {
					t.Fatal(err)
				}
			}
			d.syncProfile(profile)
			tuiTmpdir := c.setup(t)

			capture := startTUIInForeignTmuxServerWithTmpdir(t, bin, profile, tuiTmpdir)
			sawNestedTUI, childStatus := false, ""
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				if count, err := db.AliveInstanceCount(); err == nil && count >= 2 {
					sawNestedTUI = true
				}
				rows, err := db.ReadAllStatuses()
				if err != nil {
					t.Fatal(err)
				}
				childStatus = rows[child.ID].Status
				if childStatus != string(StatusRunning) {
					break
				}
			}
			if !sawNestedTUI {
				t.Fatalf("the nested TUI never registered; pane:\n%s", capture())
			}
			d.syncProfile(profile)
			events, _ := DrainInboxForParent(parentID)
			if childStatus != string(StatusRunning) || len(events) != 0 {
				t.Fatalf("healthy child: row %q, parent events %d; want running and 0 (no verdict, no write); pane:\n%s",
					childStatus, len(events), capture())
			}
		})
	}
}
