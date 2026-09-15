package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// No hooks or bound IDs: exercise the stale Codex fallback with a counting
// tmux fake. It is the only tmux on PATH and never opens a real server.
func TestListJSON_CodexProbeCount(t *testing.T) {
	sb, env, calls := listPerfFixture(t)
	started := time.Now()
	runListFixture(t, sb, env)
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), "show-environment")
	t.Logf("100 stale Codex sessions: %v; show-environment subprocesses=%d", time.Since(started), count)
	if count > 500 {
		t.Fatalf("show-environment subprocesses=%d, want <=500 (linear in 100 sessions)", count)
	}
	if strings.Contains(string(data), "CODEX_SESSION_ID") {
		t.Fatal("status listing performed native Codex session discovery")
	}
}

func TestPerf_ColdStart_List100(t *testing.T) {
	testutil.SkipIfShort(t)
	sb, env, _ := listPerfFixture(t)
	budget := testutil.ColdBudget(t, 200*time.Millisecond)
	got := testutil.TrimmedMean(func() { runListFixture(t, sb, env) })
	t.Logf("list --json 100 stale Codex sessions trimmed mean=%v budget=%v", got, budget)
	if got > budget {
		t.Fatalf("list --json 100 sessions=%v, budget=%v", got, budget)
	}
}

func listPerfFixture(t *testing.T) (*harness.Sandbox, []string, string) {
	t.Helper()
	sb := harness.NewSandbox(t)
	profileDir := filepath.Join(sb.Home, ".agent-deck", "profiles", "default")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(filepath.Join(profileDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	rows := make([]*statedb.InstanceRow, 100)
	var names, windows, panes strings.Builder
	for n := range rows {
		id := fmt.Sprintf("list-perf-%03d", n)
		name := "agentdeck_" + id
		rows[n] = &statedb.InstanceRow{
			ID: id, Title: id, ProjectPath: sb.Home, GroupPath: "my-sessions",
			Tool: "codex", Status: "waiting", TmuxSession: name,
			CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
		fmt.Fprintln(&names, name)
		fmt.Fprintf(&windows, "%s|1|0|codex\n", name)
		fmt.Fprintf(&panes, "%s|codex|0|0|0|⠋ Working\n", name)
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"names": names.String(), "windows": windows.String(), "panes": panes.String()} {
		if err := os.WriteFile(filepath.Join(sb.Home, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shim := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/tmux-calls"
while [ "$#" -gt 0 ]; do
 case "$1" in
  list-windows) cat "$HOME/windows"; exit 0;;
  list-sessions) cat "$HOME/names"; exit 0;;
  list-panes) case "$*" in *'pane_pid'*) printf '0\n';; *'-a'*) cat "$HOME/panes";; *) printf '0\n';; esac; exit 0;;
  show-environment) exit 1;;
  capture-pane) printf 'Working (esc to interrupt)\n'; exit 0;;
  has-session) exit 0;;
  display-message) printf '0\n'; exit 0;;
 esac
 shift
done
exit 0
`
	if err := os.WriteFile(filepath.Join(sb.ShimDir, "tmux"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	return sb, perfEnv(sb), filepath.Join(sb.Home, "tmux-calls")
}

func runListFixture(t *testing.T, sb *harness.Sandbox, env []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"list", "--json"}
	cmd := exec.CommandContext(ctx, sb.BinPath, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("list JSON: %v\n%s", err, out)
	}
	if len(rows) != 100 {
		t.Fatalf("rows=%d, want 100", len(rows))
	}
	for _, row := range rows {
		if row.Status != "running" {
			t.Fatalf("%s status=%q, want running from live pane rather than stored waiting", row.ID, row.Status)
		}
	}
}
