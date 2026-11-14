package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestCLITimingOpUsesOnlyCommandWords(t *testing.T) {
	cases := []struct {
		cmd  string
		rest []string
		want string
		ok   bool
	}{
		{"list", []string{"--json"}, "cli.list", true},
		{"session", []string{"send", "my session", "hello"}, "cli.session.send", true},
		{"events", []string{"publish", "x"}, "cli.events.publish", true},
		{"conductor", []string{"heartbeat-tick", "x"}, "cli.conductor.heartbeat-tick", true},
		{"add", []string{"/home/someone/project"}, "cli.add", true},
		{"session", []string{"/tmp/Secret Title"}, "cli.session", true},
		{"session", []string{"my-feature"}, "cli.session", true},
		{"inbox", []string{"my-conductor"}, "cli.inbox", true},
		{"group", []string{"create", "secret-group"}, "cli.group.create", true},
		{"session", []string{"help"}, "cli.session", true},
		{"web", nil, "", false},
		{"notify-daemon", nil, "", false},
		{"session", []string{"attach", "x"}, "", false},
		{"remote", []string{"attach", "box", "x"}, "", false},
		{"events", []string{"follow"}, "", false},
		{"--help", nil, "", false},
		{"/usr/bin/whatever", nil, "", false},
		{"not-a-command", nil, "", false},
	}
	for _, c := range cases {
		got, ok := cliTimingOp(c.cmd, c.rest)
		if got != c.want || ok != c.ok {
			t.Errorf("cliTimingOp(%q, %q) = %q, %v; want %q, %v", c.cmd, c.rest, got, ok, c.want, c.ok)
		}
	}
}

// runDeck runs the sandboxed binary with extra env and returns stdout.
func runDeck(t *testing.T, sb *harness.Sandbox, extra []string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, args...)
	cmd.Env = append(append(sb.Env(), "AGENTDECK_SUPPRESS_TMUX_WARNING=1"), extra...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, out)
	}
	return out
}

// TestPerfRecordsCLIOperationsOnlyWhenOn: with AGENTDECK_TIMINGS unset a CLI
// command leaves no timings file; with it set, `perf --json` reports the
// command as an operation with a target.
func TestPerfRecordsCLIOperationsOnlyWhenOn(t *testing.T) {
	sb := harness.NewSandbox(t)
	timings := filepath.Join(sb.Home, ".cache", "agent-deck", "timings.jsonl")

	off := []string{"AGENTDECK_TIMINGS="} // explicit, whatever the test runner exports
	runDeck(t, sb, off, "list", "--json")
	if _, err := os.Stat(timings); !os.IsNotExist(err) {
		t.Fatalf("timings file written with recording off (err=%v)", err)
	}

	on := []string{"AGENTDECK_TIMINGS=1"}
	runDeck(t, sb, on, "list", "--json")
	runDeck(t, sb, on, "list", "--json")
	out := runDeck(t, sb, off, "perf", "--json")
	var rep struct {
		Recording  bool `json:"recording"`
		Operations []struct {
			Op     string  `json:"op"`
			Count  int     `json:"count"`
			P95MS  float64 `json:"p95_ms"`
			Target float64 `json:"target_p95_ms"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("perf --json: %v\n%s", err, out)
	}
	if rep.Recording {
		t.Error("perf reports recording on without AGENTDECK_TIMINGS")
	}
	if len(rep.Operations) != 1 || rep.Operations[0].Op != "cli.list" || rep.Operations[0].Count != 2 ||
		rep.Operations[0].P95MS <= 0 || rep.Operations[0].Target != 300 {
		t.Fatalf("perf operations = %+v, want cli.list x2 with target 300ms", rep.Operations)
	}
}

// TestPerfConfigSwitchRecords: `[logs] timings = true` in config.toml turns
// recording on without the environment variable.
func TestPerfConfigSwitchRecords(t *testing.T) {
	sb := harness.NewSandbox(t)
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[logs]\ntimings = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDeck(t, sb, nil, "list", "--json")
	timings := filepath.Join(sb.Home, ".cache", "agent-deck", "timings.jsonl")
	b, err := os.ReadFile(timings)
	if err != nil || !strings.Contains(string(b), `"op":"cli.list"`) {
		t.Fatalf("timings.jsonl after list with [logs] timings = true: %q, %v", b, err)
	}
}
