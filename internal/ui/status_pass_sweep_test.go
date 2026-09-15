package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Drive the actual ten-worker sweep. Delayed per-instance env reads make the
// sweep exceed ownership's TTL; a fresh pass per worker would repeat the scan.
func TestBackgroundStatusPassOwnershipLinear(t *testing.T) {
	const n = 40
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("SWEEP_FIXTURE", dir)
	var names, windows, panes strings.Builder
	h := newHomeForSnapshotTest()
	for j := 0; j < n; j++ {
		name := fmt.Sprintf("agentdeck_background_pass_%d", j)
		fmt.Fprintln(&names, name)
		fmt.Fprintf(&windows, "%s|1|0|codex\n", name)
		fmt.Fprintf(&panes, "%s|codex|0|0|0|⠋ Working\n", name)
		inst := &session.Instance{ID: name, Title: name, Tool: "codex", Status: session.StatusWaiting, ProjectPath: dir, CreatedAt: time.Now().Add(-time.Hour)}
		inst.SetTmuxSessionForTest(&tmux.Session{Name: name, SocketName: "status-pass-fixture"})
		h.instances = append(h.instances, inst)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SWEEP_FIXTURE/calls"
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
case "$1" in
list-sessions) cat "$SWEEP_FIXTURE/names";;
list-windows) cat "$SWEEP_FIXTURE/windows";;
list-panes) case "$*" in *pane_pid*) printf '1\n';; *-a*) cat "$SWEEP_FIXTURE/panes";; *) printf '0\n';; esac;;
show-environment)
 if [ "$#" = 3 ]; then
  printf 'CODEX_SESSION_ID=owned-%s\n' "$3"
 else
  case "$*" in *CODEX_SESSION_ID*) sleep 0.8;; esac
 fi;;
has-session) exit 0;;
capture-pane) printf '⠋ Working (esc to interrupt)\n';;
display-message) printf '0\n';;
*) exit 0;;
esac
`
	for name, data := range map[string]string{"tmux": script, "names": names.String(), "windows": windows.String(), "panes": panes.String(), "ps": "#!/bin/sh\ncase \"$*\" in *args=*) printf 'sh\\n';; *) printf '1 0 sh\\n';; esac\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmux.ResetSocketSessionCacheForTest()
	t.Cleanup(tmux.ResetSocketSessionCacheForTest)
	started := time.Now()
	h.backgroundStatusUpdate()
	elapsed := time.Since(started)
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	reads := strings.Count(string(data), "show-environment")
	t.Logf("production background sweep: %d instances, %d environment reads, %s", n, reads, elapsed)
	if elapsed < 2*time.Second {
		t.Fatal("fixture did not cross ownership TTL")
	}
	if reads != 2*n {
		t.Fatalf("environment reads=%d, want %d (one own read and one shared peer read per instance)", reads, 2*n)
	}
	snapshot := h.getSessionRenderSnapshot()
	for _, inst := range h.instances {
		if inst.GetStatusThreadSafe() != session.StatusRunning || snapshot[inst.ID].status != session.StatusRunning {
			t.Errorf("%s did not finish polling and publish running status", inst.ID)
		}
	}
}
