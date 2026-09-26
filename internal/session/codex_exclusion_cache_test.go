package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func codexCountingTmux(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	var names strings.Builder
	for j := 0; j < n; j++ {
		fmt.Fprintf(&names, "agentdeck_scan_%d\n", j)
	}
	t.Setenv("CODEX_SCAN_NAMES", names.String())
	t.Setenv("CODEX_SCAN_LOG", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CODEX_SCAN_LOG"
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
case "$1" in
list-sessions)
 if [ "$CODEX_SCAN_FAIL" = 1 ]; then exit 1; fi
 if [ "$CODEX_SCAN_EMPTY" = 1 ]; then
  printf '%s' "$CODEX_SCAN_NAMES" | while IFS= read -r name; do printf '%s\t\n' "$name"; done
 else
  printf '%s' "$CODEX_SCAN_NAMES" | while IFS= read -r name; do
   case "$name" in
   *_unbound_*) printf '%s\t%s\n' "$name" "$CODEX_SCAN_GLOBAL" ;;
   *) printf '%s\tid-%s\n' "$name" "$name" ;;
   esac
  done
 fi ;;
show-environment)
 if [ "$CODEX_SCAN_FAIL" = 1 ]; then exit 1; fi
 if [ "$2" = -g ]; then
  if [ -n "$CODEX_SCAN_GLOBAL" ]; then printf 'CODEX_SESSION_ID=%s\n' "$CODEX_SCAN_GLOBAL"; exit 0; fi
  echo 'unknown variable: CODEX_SESSION_ID' >&2; exit 1
 fi
 if [ "$CODEX_SCAN_EMPTY" = 1 ]; then exit 0; fi
 case "$3" in
 *_unbound_*) exit 0 ;;
 esac
 printf 'CODEX_SESSION_ID=id-%s\n' "$3" ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestCodexExclusionScanLinear(t *testing.T) {
	resetCodexOwnershipCache(t)
	const n = 40
	log := codexCountingTmux(t, n)
	start := time.Now()
	var wg sync.WaitGroup
	for j := 0; j < n; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			name := fmt.Sprintf("agentdeck_scan_%d", j)
			inst := &Instance{tmuxSession: &tmux.Session{Name: name}}
			exclude := inst.collectOtherCodexSessionIDs()
			if len(exclude) != n-1 || exclude["id-"+name] {
				t.Errorf("wrong exclusion set for %s: %v", name, exclude)
			}
		}(j)
	}
	wg.Wait()
	elapsed := time.Since(start)
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	t.Logf("%d concurrent instances: %d tmux calls, %s", n, len(calls), elapsed)
	if len(calls) != 2 {
		t.Fatalf("exclusion scan made %d tmux calls; want one batched list plus one global environment read", len(calls))
	}
}

func resetCodexOwnershipCache(t *testing.T) {
	t.Helper()
	clear := func() {
		codexOwnershipCache.Lock()
		codexOwnershipCache.bySocket = make(map[string]codexOwnershipSnapshot)
		codexOwnershipCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func expireCodexOwnershipCache() {
	codexOwnershipCache.Lock()
	defer codexOwnershipCache.Unlock()
	for socket, snapshot := range codexOwnershipCache.bySocket {
		snapshot.at = time.Now().Add(-2 * codexExclusionTTL)
		codexOwnershipCache.bySocket[socket] = snapshot
	}
}

func TestCodexExclusionPassPinsSnapshotAndRefreshes(t *testing.T) {
	resetCodexOwnershipCache(t)
	log := codexCountingTmux(t, 2)
	inst := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0"}}
	var pass StatusUpdatePass
	first := inst.codexExclusions(&pass)
	if !first["id-agentdeck_scan_1"] {
		t.Fatal(first)
	}
	// Caller mutation must not corrupt another instance's view.
	first["injected"] = true
	if inst.codexExclusions(&pass)["injected"] {
		t.Fatal("shared mutable exclusions")
	}
	t.Setenv("CODEX_SCAN_NAMES", "agentdeck_scan_0\nagentdeck_scan_new\n")
	expireCodexOwnershipCache()
	pinned := inst.codexExclusions(&pass)
	if !pinned["id-agentdeck_scan_1"] || pinned["id-agentdeck_scan_new"] {
		t.Fatal("pass lost its snapshot", pinned)
	}
	var next StatusUpdatePass
	refreshed := inst.codexExclusions(&next)
	if refreshed["id-agentdeck_scan_1"] || !refreshed["id-agentdeck_scan_new"] {
		t.Fatal("next pass did not refresh", refreshed)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 4 {
		t.Fatalf("two scans: got %d calls, want 4 (list plus global read each)", got)
	}
}

func TestCodexExclusionSocketsAndDuplicateOwners(t *testing.T) {
	resetCodexOwnershipCache(t)
	log := codexCountingTmux(t, 2)
	var pass StatusUpdatePass
	a := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0", SocketName: "first"}}
	b := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0", SocketName: "second"}}
	a.codexExclusions(&pass)
	b.codexExclusions(&pass)
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "-L first") != 2 || strings.Count(string(data), "-L second") != 2 {
		t.Fatalf("socket routing: %s", data)
	}
	pass.bySocket["first"] = codexOwnershipSnapshot{claims: &codexOwnershipClaims{bySession: map[string]string{"agentdeck_scan_0": "shared", "agentdeck_scan_1": "shared"}}}
	if !a.codexExclusions(&pass)["shared"] {
		t.Fatal("own ID also held by peer must remain excluded")
	}
}

func TestCodexExclusionSkippedWithoutDiskScan(t *testing.T) {
	resetCodexOwnershipCache(t)
	log := codexCountingTmux(t, 2)
	for _, known := range []bool{false, true} {
		inst := &Instance{Tool: "codex", lastCodexProbeAt: time.Now(), lastCodexScanAt: time.Now()}
		if known {
			inst.CodexSessionID = "known"
		}
		inst.UpdateCodexSession(nil)
	}
	if data, err := os.ReadFile(log); err == nil {
		t.Fatalf("no disk scan needed, but tmux called: %s", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestCodexExclusionNewClaimVisibleInPinnedPass(t *testing.T) {
	resetCodexOwnershipCache(t)
	codexCountingTmux(t, 2)
	t.Setenv("CODEX_SCAN_EMPTY", "1")
	var pass StatusUpdatePass
	a := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0"}}
	b := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_1"}}
	if len(b.codexExclusions(&pass)) != 0 {
		t.Fatal("expected unbound peers")
	}
	a.recordCodexOwnership("new-binding")
	if !b.codexExclusions(&pass)["new-binding"] {
		t.Fatal("new claim missing from pinned pass")
	}
	if a.codexExclusions(&pass)["new-binding"] {
		t.Fatal("own claim excluded")
	}
	a.recordCodexOwnership("rotated")
	exclude := b.codexExclusions(&pass)
	if exclude["new-binding"] || !exclude["rotated"] {
		t.Fatal("rotation did not replace claim", exclude)
	}
}

func TestCodexExclusionBootstrapCannotClaimSameRollout(t *testing.T) {
	resetCodexOwnershipCache(t)
	codexCountingTmux(t, 2)
	t.Setenv("CODEX_SCAN_EMPTY", "1")
	t.Setenv("CODEX_HOME", t.TempDir())
	sid := uniqueSID(t)
	seedCodexRolloutWithMeta(t, os.Getenv("CODEX_HOME"), sid, "", "", false)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for j := 0; j < 2; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			inst := &Instance{Tool: "codex", ProjectPath: "/tmp/project", tmuxSession: &tmux.Session{Name: fmt.Sprintf("agentdeck_scan_%d", j)}}
			results <- inst.resolveCodexDetectionCandidate("", nil)
		}(j)
	}
	wg.Wait()
	close(results)
	claims := 0
	for got := range results {
		if got == sid {
			claims++
		} else if got != "" {
			t.Fatalf("unexpected candidate %q", got)
		}
	}
	if claims != 1 {
		t.Fatalf("rollout claimed %d times; want exactly one", claims)
	}
}

func TestCodexExclusionFailedPeerReadIsUnknown(t *testing.T) {
	resetCodexOwnershipCache(t)
	log := codexCountingTmux(t, 2)
	t.Setenv("CODEX_SCAN_FAIL", "1")
	var pass StatusUpdatePass
	inst := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0"}}
	if inst.codexExclusions(&pass) != nil {
		t.Fatal("failed peer probe must not yield a usable exclusion set")
	}
	if inst.codexExclusions(&pass) != nil {
		t.Fatal("failed pass must remain unknown")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 1 {
		t.Fatalf("failed pass re-probed: %d calls", got)
	}
}

func TestCodexExclusionAuthoritativeBindingAfterSnapshot(t *testing.T) {
	for _, source := range []string{"hook", "process"} {
		t.Run(source, func(t *testing.T) {
			resetCodexOwnershipCache(t)
			codexCountingTmux(t, 2)
			t.Setenv("CODEX_SCAN_EMPTY", "1")
			t.Setenv("CODEX_HOME", t.TempDir())
			var pass StatusUpdatePass
			a := &Instance{Tool: "codex", tmuxSession: &tmux.Session{Name: "agentdeck_scan_0"}}
			b := &Instance{Tool: "codex", tmuxSession: &tmux.Session{Name: "agentdeck_scan_1"}}
			b.codexExclusions(&pass)
			sid := uniqueSID(t)
			if source == "hook" {
				a.bindCodexSessionFromHook(sid, "agent-turn-complete")
			} else if got := a.resolveCodexDetectionCandidate(sid, nil); got != sid {
				t.Fatalf("probe candidate=%q", got)
			}
			if !b.codexExclusions(&pass)[sid] {
				t.Fatal("authoritative binding missing from pinned pass")
			}
		})
	}
}

// The tmux server's global environment can carry CODEX_SESSION_ID when the
// client that started it had one (a Codex tool shell). list-sessions formats
// resolve unbound sessions to that value; the refresh must not treat it as a
// binding, or the real owner is excluded from its own ID.
func TestCodexExclusionIgnoresGlobalEnvironmentValue(t *testing.T) {
	resetCodexOwnershipCache(t)
	log := codexCountingTmux(t, 0)
	t.Setenv("CODEX_SCAN_NAMES", "agentdeck_scan_0\nagentdeck_unbound_1\nagentdeck_unbound_2\n")
	t.Setenv("CODEX_SCAN_GLOBAL", "leaked-id")
	var pass StatusUpdatePass
	owner := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_scan_0"}}
	exclude := owner.codexExclusions(&pass)
	if exclude == nil || exclude["leaked-id"] || exclude["id-agentdeck_scan_0"] {
		t.Fatalf("global environment value leaked into exclusions: %v", exclude)
	}
	unbound := &Instance{tmuxSession: &tmux.Session{Name: "agentdeck_unbound_1"}}
	if got := unbound.codexExclusions(&pass); !got["id-agentdeck_scan_0"] || got["leaked-id"] {
		t.Fatalf("peer view wrong: %v", got)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	// One list, one global read, one per-session read per ambiguous row.
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 4 {
		t.Fatalf("calls = %d, want 4:\n%s", got, data)
	}
}
