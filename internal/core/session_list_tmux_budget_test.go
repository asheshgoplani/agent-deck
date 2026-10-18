package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// listBudgetTmux is a fake tmux that logs one line per process and answers
// the inventory, capture and display-message commands a live listing issues,
// including `;`-joined command sequences.
const listBudgetTmux = `#!/bin/sh
printf '%s\n' "$*" >> "$LIST_BUDGET/calls"
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ] || [ "$1" = -S ]; then shift 2; fi
run() {
 set -- $1
 case "$1" in
 list-sessions) cat "$LIST_BUDGET/names";;
 list-windows) while IFS= read -r n; do printf '%s|1700000000|0|@1|claude\n' "$n"; done < "$LIST_BUDGET/names";;
 list-panes) while IFS= read -r n; do printf '%s|claude|0|0|0|claude\n' "$n"; done < "$LIST_BUDGET/names";;
 capture-pane)
  t=""
  while [ $# -gt 0 ]; do if [ "$1" = -t ]; then t=$2; fi; shift; done
  printf 'frame of %s\n\n> \n' "$t";;
 display-message) last=""; for a in "$@"; do last=$a; done; printf '%s\n' "$last";;
 *) ;;
 esac
}
cur=""
for a in "$@"; do
 if [ "$a" = ";" ]; then run "$cur"; cur=""; else cur="$cur $a"; fi
done
if [ -n "$cur" ]; then run "$cur"; fi
exit 0
`

// TestSessionListLiveTmuxCallsDoNotScaleWithSessions guards the
// `list --json --stats` hot path that remote polls run every few seconds:
// the number of tmux processes a live listing starts must not grow with the
// number of live sessions. Before the batched capture every live session
// cost its own `tmux capture-pane` process, so a 150-session host paid 150
// fork+exec+server round trips per poll, multiplied by every concurrent
// poller.
func TestSessionListLiveTmuxCallsDoNotScaleWithSessions(t *testing.T) {
	captures := func(t *testing.T, n int) (int, int, []session.Substate) {
		t.Helper()
		dir := t.TempDir()
		t.Setenv("LIST_BUDGET", dir)
		profile := fmt.Sprintf("_core_list_budget_%d", n)
		instances := make([]*session.Instance, 0, n)
		var names strings.Builder
		for j := 0; j < n; j++ {
			inst := session.NewInstanceWithTool(fmt.Sprintf("budget-%d", j), t.TempDir(), "claude")
			inst.Status = session.StatusWaiting
			instances = append(instances, inst)
			fmt.Fprintln(&names, inst.GetTmuxSession().Name)
		}
		if err := os.WriteFile(filepath.Join(dir, "names"), []byte(names.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(listBudgetTmux), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		seedStore(t, profile, nil, instances...)

		out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
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
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		captureProcs := 0
		for _, line := range lines {
			if strings.Contains(line, "capture-pane") {
				captureProcs++
			}
		}
		subs := make([]session.Substate, 0, n)
		for _, row := range out.Sessions {
			subs = append(subs, session.Substate(row.Substate))
		}
		return captureProcs, len(lines), subs
	}

	smallCaptures, smallTotal, smallSubs := captures(t, 3)
	largeCaptures, largeTotal, largeSubs := captures(t, 24)
	t.Logf("3 sessions: %d capture processes, %d tmux processes; 24 sessions: %d capture processes, %d tmux processes",
		smallCaptures, smallTotal, largeCaptures, largeTotal)
	if largeCaptures > 2 {
		t.Errorf("24 live sessions started %d tmux capture-pane processes, want O(1) (one batched call per socket)", largeCaptures)
	}
	if largeTotal-smallTotal > 2 {
		t.Errorf("tmux processes grew from %d (3 sessions) to %d (24 sessions); the live listing must not spawn per session", smallTotal, largeTotal)
	}
	// The batched frames must reach every row exactly as a per-session
	// capture would: every row of one listing agrees on the same substate.
	for _, subs := range [][]session.Substate{smallSubs, largeSubs} {
		for _, s := range subs {
			if s != subs[0] {
				t.Fatalf("rows disagree on substate: %v", subs)
			}
		}
	}
}
