package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
)

// TestOptiming_SyncOnceRecordsDaemonPass: with timings on, each notify-daemon
// pass appends one daemon.pass line (ASK-106).
func TestOptiming_SyncOnceRecordsDaemonPass(t *testing.T) {
	d, _ := bootstrapDaemonProfile(t, "_test_optiming_daemon_pass")
	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, true))

	d.SyncOnce(context.Background())
	d.SyncOnce(context.Background())

	entries, err := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Op == "daemon.pass" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("daemon.pass recorded %d times, want 2 (entries %+v)", n, entries)
	}
}
