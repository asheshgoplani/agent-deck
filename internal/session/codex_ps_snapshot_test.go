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

// psSnapshotFixture fakes tmux, ps and pgrep for n Codex rows on the default
// socket. Row j's pane leader is pid 99990000+j (a shell); odd rows also run
// Codex as its child, pid 99991000+j. The pids are above any pid_max, so the
// /proc probe of a Codex candidate fails and leaves that row undetermined.
// Every ps and pgrep call is logged, one line each.
func psSnapshotFixture(t *testing.T, n int) (dir, calls string) {
	t.Helper()
	resetCodexOwnershipCache(t)
	dir = t.TempDir()
	t.Setenv("PS_FIXTURE", dir)
	var names, table strings.Builder
	for j := 0; j < n; j++ {
		fmt.Fprintf(&names, "agentdeck_ps_%d\n", j)
		// Padded like real ps output; argv keeps its inner spaces.
		fmt.Fprintf(&table, "%9d %9d /bin/sh -c  agent  wrapper %d\n", 99990000+j, 1, j)
		if j%2 == 1 {
			fmt.Fprintf(&table, "%9d %9d /opt/Tools Dir/CODEX --yolo\n", 99991000+j, 99990000+j)
		}
	}
	for name, content := range map[string]string{"names": names.String(), "table": table.String()} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tmuxScript := `#!/bin/sh
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
case "$1" in
list-sessions)
 case "$*" in
 *CODEX_SESSION_ID*) while IFS= read -r name; do printf '%s\t\n' "$name"; done < "$PS_FIXTURE/names";;
 *) cat "$PS_FIXTURE/names";;
 esac;;
show-environment) case "$*" in *CODEX_SESSION_ID*) ;; *) printf 'CODEX_SESSION_ID=\n';; esac;;
has-session) exit 0;;
capture-pane) printf '⠋ Working (esc to interrupt)\n';;
list-panes)
 case "$*" in
 *pane_pid*) t=$(printf '%s\n' "$@" | grep '^agentdeck_ps_'); printf '%s\n' $((99990000 + ${t#agentdeck_ps_}));;
 *) printf '0\n';;
 esac;;
display-message) printf '0\n';;
*) exit 0;;
esac
`
	// ps answers the whole-table forms from the table file (with or without
	// argv), and -p <pid> -o args= per pid. A "table-fails" file makes the
	// whole-table forms fail, as when ps output is unavailable; a
	// "table-hangs" file makes the pass's snapshot form (with argv) hang.
	psScript := `#!/bin/sh
printf 'ps %s\n' "$*" >> "$PS_FIXTURE/calls"
case "$*" in
*-eo*)
 if [ -f "$PS_FIXTURE/table-fails" ]; then exit 1; fi
 case "$*" in *args=*) if [ -f "$PS_FIXTURE/table-hangs" ]; then exec sleep 60; fi;; esac
 case "$*" in
 *args=*) cat "$PS_FIXTURE/table";;
 *) awk '{print $1, $2}' "$PS_FIXTURE/table";;
 esac;;
*args=*)
 pid=$2
 line=$(awk -v p="$pid" '$1 == p' "$PS_FIXTURE/table")
 if [ -z "$line" ]; then exit 1; fi
 printf '%s\n' "$line" | sed -E 's/^ *[0-9]+ +[0-9]+ //';;
*) exit 1;;
esac
`
	pgrepScript := `#!/bin/sh
printf 'pgrep %s\n' "$*" >> "$PS_FIXTURE/calls"
out=$(awk -v p="$2" '$2 == p {print $1}' "$PS_FIXTURE/table")
if [ -z "$out" ]; then exit 1; fi
printf '%s\n' "$out"
`
	for name, script := range map[string]string{"tmux": tmuxScript, "ps": psScript, "pgrep": pgrepScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, filepath.Join(dir, "calls")
}

// psSnapshotRows returns n unbound Codex rows, each in its own project with
// one user rollout there, so a row the process probe finds without Codex
// binds its own rollout from the bootstrap disk scan, and a row running Codex
// stays unbound (its probe is undetermined).
func psSnapshotRows(t *testing.T, n int) ([]*Instance, []string) {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	rows := make([]*Instance, n)
	rollouts := make([]string, n)
	for j := range rows {
		project := filepath.Join(t.TempDir(), fmt.Sprintf("project-%d", j))
		rollouts[j] = fmt.Sprintf("019f0000-0000-7000-8000-%012d", 900000+j)
		seedProjectRollout(t, codexHome, rollouts[j], project)
		rows[j] = &Instance{ID: fmt.Sprintf("ps-%d", j), Tool: "codex", Status: StatusRunning,
			ProjectPath: project, CreatedAt: time.Now().Add(-time.Hour),
			CodexStartedAt: time.Now().Add(-time.Minute).UnixMilli(),
			tmuxSession:    &tmux.Session{Name: fmt.Sprintf("agentdeck_ps_%d", j), Command: "codex"}}
	}
	return rows, rollouts
}

// runStatusPass updates every row on one pass with four concurrent workers,
// like the web menu and TUI sweeps.
func runStatusPass(t *testing.T, rows []*Instance) {
	t.Helper()
	var pass StatusUpdatePass
	var wg sync.WaitGroup
	work := make(chan *Instance)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for inst := range work {
				if err := pass.UpdateStatus(inst); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for _, inst := range rows {
		work <- inst
	}
	close(work)
	wg.Wait()
}

func readCalls(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(strings.ReplaceAll(string(data), " ", "_"))
}

// checkPassBindings asserts each row's result came from its own pane tree:
// rows without Codex bound their rollout, rows with Codex stayed unbound.
func checkPassBindings(t *testing.T, rows []*Instance, rollouts []string) {
	t.Helper()
	for j, inst := range rows {
		inst.mu.Lock()
		got := inst.CodexSessionID
		inst.mu.Unlock()
		want := rollouts[j]
		if j%2 == 1 {
			want = ""
		}
		if got != want {
			t.Errorf("row %d (codex child=%v): CodexSessionID = %q, want %q", j, j%2 == 1, got, want)
		}
	}
}

// One status pass reads the process table once for all of its Codex rows,
// instead of `ps -eo` per probe per row plus `ps -p <pid> -o args=` per pane
// pid (ASK-106: about half the web menu's subprocess time).
func TestStatusPassCodexProbesShareOneProcessSnapshot(t *testing.T) {
	const n = 8
	_, calls := psSnapshotFixture(t, n)
	rows, rollouts := psSnapshotRows(t, n)

	runStatusPass(t, rows)

	got := readCalls(t, calls)
	t.Logf("%d Codex rows, one pass: %d ps/pgrep calls: %v", n, len(got), got)
	if len(got) != 1 || !strings.HasPrefix(got[0], "ps_") || !strings.Contains(got[0], "-eo") {
		t.Errorf("ps/pgrep calls for one pass = %d %v, want exactly one whole-table ps", len(got), got)
	}
	checkPassBindings(t, rows, rollouts)

	// The next pass takes its own snapshot.
	next, _ := psSnapshotRows(t, n)
	runStatusPass(t, next)
	if again := readCalls(t, calls); len(again) != 2 {
		t.Errorf("ps/pgrep calls after a second pass = %d %v, want 2 (one per pass)", len(again), again)
	}
}

// When the whole-table ps fails, every row still falls back to pgrep and
// per-pid ps, with the same results; the failed read is not retried per row.
func TestStatusPassProcessSnapshotFailureFallsBackPerRow(t *testing.T) {
	const n = 6
	dir, calls := psSnapshotFixture(t, n)
	if err := os.WriteFile(filepath.Join(dir, "table-fails"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rows, rollouts := psSnapshotRows(t, n)

	runStatusPass(t, rows)

	tables, pgreps := 0, 0
	for _, call := range readCalls(t, calls) {
		switch {
		case strings.HasPrefix(call, "ps_") && strings.Contains(call, "-eo"):
			tables++
		case strings.HasPrefix(call, "pgrep_"):
			pgreps++
		}
	}
	t.Logf("failed whole-table ps: %d attempts, %d pgrep calls", tables, pgreps)
	if tables != 1 {
		t.Errorf("whole-table ps attempts in one pass = %d, want 1", tables)
	}
	if pgreps == 0 {
		t.Error("rows did not fall back to pgrep after ps failed")
	}
	checkPassBindings(t, rows, rollouts)
}

// A hung whole-table ps is bounded: the pass gives up on the snapshot after
// psSnapshotTimeout and every row falls back per row, instead of every worker
// of the pass waiting on the one ps forever.
func TestStatusPassProcessSnapshotHangIsBounded(t *testing.T) {
	const n = 4
	dir, calls := psSnapshotFixture(t, n)
	if err := os.WriteFile(filepath.Join(dir, "table-hangs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	saved := psSnapshotTimeout
	psSnapshotTimeout = 300 * time.Millisecond
	t.Cleanup(func() { psSnapshotTimeout = saved })
	rows, rollouts := psSnapshotRows(t, n)

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		runStatusPass(t, rows)
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		t.Logf("pass with a hung ps snapshot took %v", took)
	case <-time.After(20 * time.Second):
		t.Fatal("status pass still waiting on a hung ps after 20s")
	}

	pgreps := 0
	for _, call := range readCalls(t, calls) {
		if strings.HasPrefix(call, "pgrep_") {
			pgreps++
		}
	}
	if pgreps == 0 {
		t.Error("rows did not fall back to pgrep after the ps snapshot timed out")
	}
	checkPassBindings(t, rows, rollouts)
}

// Without a pass (async detection, the send path), every probe still reads
// the process table itself.
func TestCodexProbeWithoutPassReadsProcessTableEachTime(t *testing.T) {
	_, calls := psSnapshotFixture(t, 2)
	rows, _ := psSnapshotRows(t, 2)
	for k := 0; k < 2; k++ {
		if _, err := rows[1].PaneProcessTreePIDs(); err != nil {
			t.Fatal(err)
		}
	}
	tables := 0
	for _, call := range readCalls(t, calls) {
		if strings.Contains(call, "-eo") {
			tables++
		}
	}
	if tables != 2 {
		t.Errorf("whole-table ps reads for two probes without a pass = %d, want 2", tables)
	}
}

// The shared snapshot carries argv (-ww, untruncated): a row longer than
// bufio.Scanner's 64 KiB token must not cut the table short, and malformed
// rows still make it incomplete, as parsePSParentChildMap reports them.
func TestSplitPSArgsKeepsLongArgvAndRowErrors(t *testing.T) {
	long := "node " + strings.Repeat("x", 100<<10) + " codex"
	out := []byte("    10     1 /bin/sh -c  two  spaces\n    11    10 " + long + "\n    12    11 codex\n")
	table, args := splitPSArgs(out)
	children, err := parsePSParentChildMap(table)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if got, want := fmt.Sprint(children), fmt.Sprint(map[int][]int{1: {10}, 10: {11}, 11: {12}}); got != want {
		t.Fatalf("children = %s, want %s", got, want)
	}
	if args[10] != "/bin/sh -c  two  spaces" || args[11] != long || args[12] != "codex" {
		t.Fatalf("args = %q / len %d / %q", args[10], len(args[11]), args[12])
	}
	if !isCodexArgs(args[11]) || isCodexArgs(args[10]) {
		t.Fatal("argv classification differs from ps -p <pid> -o args=")
	}

	table, _ = splitPSArgs([]byte("10 1 sh\nbad\n    12\n"))
	_, err = parsePSParentChildMap(table)
	if err == nil {
		t.Fatal("malformed ps rows must make the snapshot incomplete")
	}
	// A short row is quoted as ps printed it (less padding), not as rebuilt.
	if want := `invalid ps row 3: "12"`; !strings.Contains(err.Error(), want) {
		t.Errorf("parse error = %q, want it to contain %q", err, want)
	}
}
