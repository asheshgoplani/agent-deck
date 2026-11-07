package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// PaneProcessTreePIDs (the send path, no status pass) must not block its
// caller on a hung ps or pgrep: both the whole-table ps and the per-pid pgrep
// fallback run under a deadline, and the probe reports an incomplete forest
// instead of waiting forever (issue #3026).
func TestPaneProcessTreePIDsHungPSAndPgrepAreBounded(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("tmux", `#!/bin/sh
case " $* " in
*" has-session "*) exit 0 ;;
*" list-panes "*) printf '99999990\n'; exit 0 ;;
esac
exit 1
`)
	// exec so the deadline's kill reaches the sleeping process itself.
	write("ps", "#!/bin/sh\nexec sleep 20\n")
	write("pgrep", "#!/bin/sh\nexec sleep 20\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	savedPS, savedPgrep := psSnapshotTimeout, pgrepFallbackTimeout
	psSnapshotTimeout = 200 * time.Millisecond
	pgrepFallbackTimeout = 200 * time.Millisecond
	t.Cleanup(func() { psSnapshotTimeout, pgrepFallbackTimeout = savedPS, savedPgrep })

	inst := &Instance{
		TmuxSocketName: "test-socket",
		tmuxSession:    &tmux.Session{Name: "hung-probe", SocketName: "test-socket"},
	}

	type result struct {
		pids []int
		err  error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		pids, err := inst.PaneProcessTreePIDs()
		done <- result{pids, err}
	}()
	select {
	case r := <-done:
		t.Logf("probe with hung ps and pgrep returned after %v", time.Since(start))
		if r.err == nil {
			t.Fatalf("hung ps/pgrep must make the pane tree incomplete, got pids %v and no error", r.pids)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("PaneProcessTreePIDs still blocked on a hung ps/pgrep after 8s")
	}
}
