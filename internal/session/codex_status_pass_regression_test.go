package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The gate stalls a full peer-environment read, not the instance's own lookup.
// Files synchronize subprocesses without racing environment changes.
func statusPassFixture(t *testing.T, n int) (string, string) {
	t.Helper()
	resetCodexOwnershipCache(t)
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("STATUS_FIXTURE", dir)
	var names strings.Builder
	for j := 0; j < n; j++ {
		fmt.Fprintf(&names, "agentdeck_pass_%d\n", j)
	}
	if err := os.WriteFile(filepath.Join(dir, "names"), []byte(names.String()), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STATUS_FIXTURE/calls"
if [ "$1" = -u ]; then shift; fi
socket=default
if [ "$1" = -L ]; then socket=$2; shift 2; fi
case "$1" in
list-sessions) cat "$STATUS_FIXTURE/names";;
show-environment)
 case "$*" in
 *CODEX_SESSION_ID*) if [ -f "$STATUS_FIXTURE/rotation" ]; then cat "$STATUS_FIXTURE/rotation"; fi;;
 *)
 if [ "$socket" = slow ] && [ -f "$STATUS_FIXTURE/gate" ]; then
  touch "$STATUS_FIXTURE/entered"
  while [ -f "$STATUS_FIXTURE/gate" ]; do sleep 0.02; done
 fi
 printf 'CODEX_SESSION_ID=\n';;
 esac;;
has-session) exit 0;;
capture-pane) printf '⠋ Working (esc to interrupt)\n';;
list-panes) printf '1\n';;
display-message) printf '0\n';;
*) exit 0;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\ncase \"$*\" in *args=*) printf 'sh\\n';; *) printf '1 0 sh\\n';; esac\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, filepath.Join(dir, "calls")
}

func passInstance(n int, socket string) *Instance {
	return &Instance{ID: fmt.Sprintf("pass-%d", n), Tool: "codex", Status: StatusRunning,
		ProjectPath: "/no-matching-rollout", CreatedAt: time.Now().Add(-time.Hour),
		lastCodexProbeAt: time.Now(),
		tmuxSession:      &tmux.Session{Name: fmt.Sprintf("agentdeck_pass_%d", n), SocketName: socket}}
}

func waitFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fixture did not reach %s", filepath.Base(path))
}

func releaseFixture(t *testing.T, dir string) {
	t.Helper()
	// Rename releases the shell gate while retaining its contents as evidence.
	if err := os.Rename(filepath.Join(dir, "gate"), filepath.Join(dir, "released")); err != nil {
		t.Fatal(err)
	}
}

func TestStatusPassRefreshDoesNotBlockReadersOrRotation(t *testing.T) {
	for _, socket := range []string{"other", "slow"} {
		t.Run(socket, func(t *testing.T) {
			dir, _ := statusPassFixture(t, 2)
			if err := os.WriteFile(filepath.Join(dir, "gate"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			var pass StatusUpdatePass
			bootstrap := passInstance(0, "slow")
			done := make(chan struct{})
			var updateErr error
			go func() { updateErr = pass.UpdateStatus(bootstrap); close(done) }()
			t.Cleanup(func() {
				if _, err := os.Stat(filepath.Join(dir, "gate")); err == nil {
					releaseFixture(t, dir)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("bootstrap worker did not exit")
				}
			})
			waitFixtureFile(t, filepath.Join(dir, "entered"))
			known := passInstance(1, socket)
			known.CodexSessionID = "old"
			known.hookStatus = "running"
			known.hookLastUpdate = time.Now()
			known.hookSessionID = "rotated-from-hook"
			updated := make(chan struct{})
			var knownErr error
			go func() { knownErr = pass.UpdateStatus(known); close(updated) }()
			read := make(chan struct{})
			go func() { bootstrap.GetStatusThreadSafe(); known.GetStatusThreadSafe(); close(read) }()
			select {
			case <-read:
			case <-time.After(300 * time.Millisecond):
				t.Error("instance status readers blocked behind peer ownership refresh")
			}
			select {
			case <-updated:
				if knownErr != nil {
					t.Error(knownErr)
				}
			case <-time.After(300 * time.Millisecond):
				t.Error("authoritative hook rotation blocked behind peer ownership refresh")
			}
			releaseFixture(t, dir)
			<-done
			<-updated
			<-read
			if updateErr != nil {
				t.Error(updateErr)
			}
			// All workers are joined even on the failing implementation.
			known.mu.Lock()
			if known.CodexSessionID != "rotated-from-hook" {
				t.Error("hook ID was not discovered")
			}
			known.mu.Unlock()
			if socket == "slow" && !bootstrap.codexExclusions(&pass)["rotated-from-hook"] {
				t.Error("refresh overwrote concurrent authoritative publication")
			}
		})
	}
}

func TestStatusPassSweepPinsOwnershipBeyondTTL(t *testing.T) {
	_, log := statusPassFixture(t, 12)
	var pass StatusUpdatePass
	start := time.Now()
	for j := 0; j < 12; j++ {
		if j == 6 {
			time.Sleep(codexExclusionTTL + 50*time.Millisecond)
		}
		inst := passInstance(j, "")
		if err := pass.UpdateStatus(inst); err != nil {
			t.Fatal(err)
		}
		if inst.GetStatusThreadSafe() != StatusRunning {
			t.Fatalf("instance %d did not take live status path", j)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	scans, peers := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "list-sessions") {
			scans++
		}
		if strings.Contains(line, "show-environment") {
			peers++
		}
	}
	t.Logf("12 production UpdateStatus calls across TTL: scans=%d environment reads=%d elapsed=%s", scans, peers, time.Since(start))
	if scans != 1 || peers != 24 {
		t.Fatalf("ownership sweep scans=%d environment reads=%d, want 1 and 24", scans, peers)
	}
}

func TestStatusPassKnownEnvironmentRotation(t *testing.T) {
	dir, _ := statusPassFixture(t, 2)
	inst := passInstance(1, "")
	inst.CodexSessionID = "old"
	writeID := func(id string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "rotation"), []byte("CODEX_SESSION_ID="+id+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeID("old")
	var first StatusUpdatePass
	if err := first.UpdateStatus(inst); err != nil {
		t.Fatal(err)
	}
	writeID("new-authoritative-id")
	start := time.Now()
	// The existing positive tmux environment TTL is 30s. Poll normally until
	// that cache and the 2s metadata cadence allow the new authoritative ID.
	deadline := start.Add(codexRotationScanInterval + 3*time.Second)
	for time.Now().Before(deadline) {
		var pass StatusUpdatePass
		if err := pass.UpdateStatus(inst); err != nil {
			t.Fatal(err)
		}
		if inst.CodexSessionID == "new-authoritative-id" {
			t.Logf("cached authoritative rotation discovered after %s", time.Since(start))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("rotation not discovered within existing 30s cache plus metadata cadence: %q", inst.CodexSessionID)
}
