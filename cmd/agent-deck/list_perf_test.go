package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// No hooks or bound IDs: exercise the stale Codex fallback with a counting
// tmux fake. It is the only tmux on PATH and never opens a real server.
func TestListJSON_CodexProbeCount(t *testing.T) {
	sb, env, calls := listPerfFixture(t)
	started := time.Now()
	defer func() {
		data, err := os.ReadFile(calls)
		if err != nil {
			t.Error(err)
			return
		}
		count := strings.Count(string(data), "show-environment")
		t.Logf("100 stale Codex sessions: %v; show-environment subprocesses=%d", time.Since(started), count)
		if count > 500 {
			t.Errorf("show-environment subprocesses=%d, want <=500 (linear in 100 sessions)", count)
		}
		if strings.Contains(string(data), "CODEX_SESSION_ID") {
			t.Error("status listing performed native Codex session discovery")
		}
	}()
	runListFixture(t, sb, env)
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

func TestListJSON_HistoricalErrorRowsUseSocketInventory(t *testing.T) {
	const count = 100
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
	rows := make([]*statedb.InstanceRow, count)
	for n := range rows {
		id := fmt.Sprintf("history-error-%03d", n)
		rows[n] = &statedb.InstanceRow{
			ID: id, Title: id, ProjectPath: sb.Home, GroupPath: "my-sessions",
			Tool: "shell", Status: "error", TmuxSession: "agentdeck_" + id,
			TmuxSocketName: "list-history-regression-absent",
			// last_started_at: these rows ran (an errored row did); without
			// the stamp a reloaded row reads as never started.
			CreatedAt: time.Now().Add(-time.Hour),
			ToolData:  session.WriteLastStartedAtToToolData(json.RawMessage(`{}`), time.Now().Add(-time.Hour)),
		}
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	calls := filepath.Join(sb.Home, "tmux-calls")
	shim := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/tmux-calls"
printf 'no server running on /tmp/agent-deck-history.sock\n' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(sb.ShimDir, "tmux"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, "list", "--json")
	cmd.Env = perfEnv(sb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var listed []struct {
		Status       string `json:"status"`
		StatusSource string `json:"status_source"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("list JSON: %v\n%s", err, out)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	inventories, probes := 0, 0
	for _, call := range strings.Split(string(data), "\n") {
		if strings.Contains(call, "list-history-regression-absent") && strings.Contains(call, "list-sessions") {
			inventories++
		}
		if strings.Contains(call, "has-session") {
			probes++
		}
	}
	t.Logf("100 synthetic missing error sessions: list=%s socket-inventories=%d has-session-probes=%d", time.Since(started), inventories, probes)
	if len(listed) != count {
		t.Fatalf("rows=%d, want %d", len(listed), count)
	}
	for i, row := range listed {
		if row.Status != "error" || row.StatusSource != "cached" {
			t.Fatalf("row %d status/source=%q/%q, want error/cached", i, row.Status, row.StatusSource)
		}
	}
	if inventories != 1 || probes != 0 {
		t.Fatalf("socket inventories=%d has-session probes=%d, want 1 and 0", inventories, probes)
	}
}

func listPerfFixture(t *testing.T) (*harness.Sandbox, []string, string) {
	t.Helper()
	tools := make([]string, 100)
	for n := range tools {
		tools[n] = "codex"
	}
	return sessionFixture(t, tools...)
}

// sessionFixture seeds one session per tool behind recording tmux and opencode fakes.
func sessionFixture(t *testing.T, tools ...string) (*harness.Sandbox, []string, string) {
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
	rows := make([]*statedb.InstanceRow, len(tools))
	var names, windows, panes strings.Builder
	for n := range rows {
		tool := tools[n]
		id := fmt.Sprintf("list-perf-%03d", n)
		name := "agentdeck_" + id
		rows[n] = &statedb.InstanceRow{
			ID: id, Title: id, ProjectPath: sb.Home, GroupPath: "my-sessions",
			Tool: tool, Command: tool, Status: "waiting", TmuxSession: name,
			CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
		fmt.Fprintln(&names, name)
		fmt.Fprintf(&windows, "%s|1|0|%s\n", name, tool)
		fmt.Fprintf(&panes, "%s|%s|0|0|0|⠋ Working\n", name, tool)
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
  capture-pane) printf '⠋ Working (esc to interrupt)\n'; exit 0;;
  has-session) exit 0;;
  display-message) case "$*" in *window_activity*) printf '1\n';; *) printf '0\n';; esac; exit 0;;
 esac
 shift
done
exit 0
`
	if err := os.WriteFile(filepath.Join(sb.ShimDir, "tmux"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	opencodeShim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/opencode-calls\"\nprintf '[]\\n'\n"
	if err := os.WriteFile(filepath.Join(sb.ShimDir, "opencode"), []byte(opencodeShim), 0700); err != nil {
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

// status must count without session discovery, and -v must reuse the counting pass.
func TestStatusJSON_NoSessionDiscovery(t *testing.T) {
	plain := runStatusFixture(t, statusFixtureTools, nil, "--json")
	verbose := runStatusFixture(t, statusFixtureTools, nil, "--json", "-v")
	quietVerbose := runStatusFixture(t, statusFixtureTools, nil, "-q", "-v")
	t.Logf("tmux calls: --json=%d --json -v=%d -q -v=%d (%d sessions)",
		len(plain), len(verbose), len(quietVerbose), len(statusFixtureTools))
	for name, calls := range map[string][]string{"--json": plain, "--json -v": verbose} {
		for _, banned := range []string{"set-environment", "show-environment"} {
			if n := countCalls(calls, banned); n != 0 {
				t.Errorf("status %s made %d %s calls with no hook payload", name, n, banned)
			}
		}
	}
	// list-* calls are the refresh and answer pane existence. capture-pane is the per-row substate read.
	captures := func(calls []string) (n int, refresh []string) {
		for _, c := range calls {
			switch {
			case strings.Contains(c, "capture-pane"):
				n++
			case strings.Contains(c, " list-"):
				refresh = append(refresh, c)
			}
		}
		return n, refresh
	}
	for name, calls := range map[string][]string{"--json": plain, "--json -v": verbose, "-q -v": quietVerbose} {
		if n := countCalls(calls, "has-session"); n != 0 {
			t.Errorf("status %s made %d has-session calls, want 0: the list-* refresh answers existence", name, n)
		}
	}
	plainCaptures, plainRest := captures(plain)
	verboseCaptures, verboseRest := captures(verbose)
	quietCaptures, _ := captures(quietVerbose)
	if strings.Join(verboseRest, "\n") != strings.Join(plainRest, "\n") {
		t.Errorf("--json -v refreshed differently from --json:\n-v: %q\nplain: %q", verboseRest, plainRest)
	}
	if verboseCaptures-plainCaptures != len(statusFixtureTools) {
		t.Errorf("--json -v captures=%d, --json=%d, want one extra per row (%d)", verboseCaptures, plainCaptures, len(statusFixtureTools))
	}
	if quietCaptures != plainCaptures {
		t.Errorf("-q -v captures=%d, want %d like --json: quiet prints no rows", quietCaptures, plainCaptures)
	}
}

// A hook-reported session ID still binds once, the same as in the TUI.
func TestStatusJSON_HookRebindWritesOnce(t *testing.T) {
	for tool, envVar := range map[string]string{"claude": "CLAUDE_SESSION_ID", "codex": "CODEX_SESSION_ID", "gemini": "GEMINI_SESSION_ID"} {
		for _, args := range [][]string{{"--json"}, {"--json", "-v"}} {
			t.Run(tool+" "+strings.Join(args, " "), func(t *testing.T) {
				hook := fmt.Sprintf(`{"status":"running","session_id":"hook-new-id","event":"UserPromptSubmit","ts":%d}`, time.Now().Unix())
				calls := runStatusFixture(t, []string{tool}, map[string]string{"list-perf-000": hook}, args...)
				if n := countCalls(calls, "set-environment"); n != 1 {
					t.Errorf("set-environment calls=%d, want 1: %q", n, calls)
				}
				if n := countCalls(calls, "set-environment", envVar, "hook-new-id"); n != 1 {
					t.Errorf("set-environment %s hook-new-id calls=%d, want 1", envVar, n)
				}
			})
		}
	}
}

func TestCountByStatusWithSubstates(t *testing.T) {
	newInsts := func() []*session.Instance {
		return []*session.Instance{{ID: "e"}, {ID: "q", Status: session.StatusQueued}}
	}
	if counts, subs := countByStatus(newInsts(), nil); subs != nil || counts.total != 2 {
		t.Fatalf("no substates: counts=%+v subs=%v, want total=2 and nil map", counts, subs)
	}
	insts := newInsts()
	counts, subs := countByStatus(insts, anyStatus)
	if counts.total != 2 || len(subs) != 2 || subs[insts[0]] != session.SubstateNone || subs[insts[1]] != session.SubstateNone {
		t.Fatalf("all statuses: counts=%+v subs=%v, want SubstateNone for both pane-less sessions", counts, subs)
	}
}

var statusFixtureTools = []string{
	"codex", "codex", "codex", "codex", "codex", "codex", "codex", "codex",
	"opencode", "opencode", "claude", "claude", "gemini", "gemini",
}

func countCalls(calls []string, parts ...string) int {
	n := 0
	for _, c := range calls {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(c, p) }) {
			n++
		}
	}
	return n
}

// runStatusFixture runs status against a fresh fixture with optional hook files and returns its tmux calls.
func runStatusFixture(t *testing.T, tools []string, hooks map[string]string, args ...string) []string {
	t.Helper()
	sb, env, calls := sessionFixture(t, tools...)
	hooksDir := filepath.Join(sb.Home, ".agent-deck", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, body := range hooks {
		if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, append([]string{"status"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status %v: %v\n%s", args, err, out)
	}
	if slices.Contains(args, "--json") {
		var resp struct {
			Error    int               `json:"error"`
			Stopped  int               `json:"stopped"`
			Total    int               `json:"total"`
			Sessions []json.RawMessage `json:"sessions"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("status %v JSON: %v\n%s", args, err, out)
		}
		if resp.Total != len(tools) || resp.Error != 0 || resp.Stopped != 0 {
			t.Errorf("status %v total=%d error=%d stopped=%d, want %d live sessions", args, resp.Total, resp.Error, resp.Stopped, len(tools))
		}
		if slices.Contains(args, "-v") && len(resp.Sessions) != resp.Total {
			t.Errorf("status %v sessions=%d, total=%d; rows and counts must cover the same set", args, len(resp.Sessions), resp.Total)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "CODEX_SESSION_ID"); n != 0 && hooks == nil {
		t.Errorf("status %v made %d tmux calls naming CODEX_SESSION_ID", args, n)
	}
	if oc, err := os.ReadFile(filepath.Join(sb.Home, "opencode-calls")); err == nil {
		t.Errorf("status %v ran opencode:\n%s", args, oc)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
