//go:build opbench

package main

// Guards for the BenchmarkOp_CLIList stand-in. Built only with -tags opbench,
// like the helpers it uses. Kept out of
// opbench_cli_test.go because bench/ops/run.sh copies that file into older
// releases, and this test reads the `list --stats` line those may lack.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// opbListRun runs `agent-deck list --json --stats` and returns stdout and the
// decoded stats line from stderr.
func opbListRun(t *testing.T, sb *opbSandbox) ([]byte, opbListStats) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.bin, "list", "--json", "--stats")
	cmd.Env = sb.env
	cmd.Dir = filepath.Join(sb.home, "work")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("list --json --stats: %v\n%s", err, stderr.String())
	}
	const prefix = "agent-deck-list-stats: "
	var stats opbListStats
	found := false
	for _, line := range strings.Split(stderr.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			if err := json.Unmarshal([]byte(rest), &stats); err != nil {
				t.Fatalf("stats line %q: %v", line, err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q line on stderr:\n%s", prefix, stderr.String())
	}
	return stdout.Bytes(), stats
}

type opbListStats struct {
	TmuxCalls int64 `json:"tmux_calls"`
	Sessions  int   `json:"sessions"`
}

// TestOpBench_CLIListStandInAnswersBatchedCapture: the list benchmark's tmux
// stand-in must answer the batched capture like real tmux does, so a status
// pass over 100 sessions costs a handful of tmux processes (the inventory
// queries plus one capture per 64 panes), not one per session. With a
// stand-in that answers only the first command of the sequence this was
// about 106 tmux processes at 100 sessions and the benchmark timed those
// forks. Each pane's frame decides its row's status (busy panes, names
// ending in 0 or 5, are running; prompts are not), and the listing must equal
// the one the per-session fallback produces, so the stand-in changes the
// cost, not the answer, and every frame reaches its own session.
func TestOpBench_CLIListStandInAnswersBatchedCapture(t *testing.T) {
	const n = 100
	sb := opbNewSandbox(t, true, "")
	names := opbSeedRows(t, sb.home, sb.socket, n)
	opbListShim(t, sb, names)

	batched, stats := opbListRun(t, sb)
	rows := opbListRows(t, batched)
	if len(rows) != n {
		t.Fatalf("list --json gave %d rows, want %d", len(rows), n)
	}
	for _, r := range rows {
		name, _ := r["tmux_session"].(string)
		busy := strings.HasSuffix(name, "0") || strings.HasSuffix(name, "5")
		if got := r["status"] == "running"; got != busy {
			t.Errorf("%s status=%v, want running=%v (from its own pane frame)", name, r["status"], busy)
		}
	}
	if stats.Sessions != n {
		t.Fatalf("stats sessions=%d, want %d", stats.Sessions, n)
	}
	const maxCalls = 16
	if stats.TmuxCalls > maxCalls {
		t.Errorf("list --json over %d sessions ran %d tmux processes, want <= %d (the stand-in must answer the batched capture)", n, stats.TmuxCalls, maxCalls)
	}

	// Same rows with a stand-in that refuses the batch: every session is
	// then captured on its own. The listing must not change.
	shimPath := filepath.Join(sb.shim, "tmux")
	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	refuse := strings.Replace(string(data), "#!/bin/sh\n", "#!/bin/sh\ncase \"$*\" in *' ; display-message -p '*) exit 1;; esac\n", 1)
	if err := os.WriteFile(shimPath, []byte(refuse), 0o700); err != nil {
		t.Fatal(err)
	}
	single, singleStats := opbListRun(t, sb)
	if singleStats.TmuxCalls < n {
		t.Fatalf("refusing stand-in ran %d tmux processes, want >= %d (per-session fallback)", singleStats.TmuxCalls, n)
	}
	if !reflect.DeepEqual(rows, opbListRows(t, single)) {
		t.Errorf("list --json differs between batched and per-session capture\nbatched:\n%.600s\nsingle:\n%.600s", batched, single)
	}
}

// opbListRows decodes a `list --json` listing. A running row's
// last_activity_at is the wall-clock time its pane was read, so it is
// replaced by whether it is set.
func opbListRows(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("list --json: %v\n%.400s", err, out)
	}
	for _, r := range rows {
		if v, ok := r["last_activity_at"]; ok {
			r["last_activity_at"] = v != ""
		}
	}
	return rows
}
