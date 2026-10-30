package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// traceFiles lists every trace.db* and trace-spool file under home.
func traceFiles(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(info.Name(), "trace.db") || strings.Contains(p, string(filepath.Separator)+"trace-spool"+string(filepath.Separator)) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

var traceE2EPayloads = []string{
	`{"hook_event_name":"SessionStart","session_id":"e2e-sess","source":"startup"}`,
	`{"hook_event_name":"UserPromptSubmit","session_id":"e2e-sess","prompt":"deploy with sk-ant-SENTINEL123456789abcdef"}`,
	`{"hook_event_name":"PreToolUse","session_id":"e2e-sess","tool_name":"Bash","tool_use_id":"toolu_e1","tool_input":{"command":"ghp_SENTINEL0123456789abcdefABCDEFxyz push"}}`,
	`{"hook_event_name":"PostToolUse","session_id":"e2e-sess","tool_name":"Bash","tool_use_id":"toolu_e1","tool_response":{"stdout":"sk-ant-SENTINEL123456789abcdef"}}`,
	`{"hook_event_name":"Stop","session_id":"e2e-sess","stop_hook_active":false,"last_assistant_message":"ok"}`,
}

// End to end through the real binary: hook-handler spools (redacted), the
// trace CLI ingests, shows the tree and the stats, and no sentinel secret
// reaches any trace file.
func TestTraceCLIHookToStatsEndToEnd(t *testing.T) {
	home := t.TempDir()
	env := []string{"AGENTDECK_TRACE=summaries", "AGENTDECK_INSTANCE_ID=inst-trace-e2e", "AGENTDECK_TOOL=claude"}
	for _, p := range traceE2EPayloads {
		if _, stderr, code := runAgentDeckEnv(t, home, p, env, "hook-handler"); code != 0 {
			t.Fatalf("hook-handler exit %d: %s", code, stderr)
		}
	}
	files := traceFiles(t, home)
	if len(files) != 1 || !strings.HasSuffix(files[0], "spool.jsonl") {
		t.Fatalf("expected only the live spool after hooks, got %v", files)
	}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "trace", "ingest", "--json")
	if code != 0 {
		t.Fatalf("trace ingest exit %d: %s", code, stderr)
	}
	var res struct{ Applied, Bad int }
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.Applied != len(traceE2EPayloads) || res.Bad != 0 {
		t.Fatalf("ingest result %q err %v", stdout, err)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "trace", "e2e-sess")
	if code != 0 || !strings.Contains(stdout, "claude:e2e-sess") || !strings.Contains(stdout, "turn") ||
		!strings.Contains(stdout, "Bash") || !strings.Contains(stdout, "ok") {
		t.Fatalf("trace tree exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "trace", "stats", "--json")
	if code != 0 {
		t.Fatalf("trace stats exit %d: %s", code, stderr)
	}
	var stats []struct {
		Tool  string
		Count int
	}
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil || len(stats) != 1 || stats[0].Tool != "Bash" || stats[0].Count != 1 {
		t.Fatalf("stats %q err %v", stdout, err)
	}
	for _, f := range traceFiles(t, home) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("SENTINEL")) {
			t.Fatalf("sentinel secret in %s", filepath.Base(f))
		}
	}
}

// Level off (the default): the hook handler and codex-notify write no trace
// file at all.
func TestTraceHookLevelOffWritesNothing(t *testing.T) {
	home := t.TempDir()
	env := []string{"AGENTDECK_INSTANCE_ID=inst-trace-off", "AGENTDECK_TOOL=claude"}
	for _, p := range traceE2EPayloads {
		if _, stderr, code := runAgentDeckEnv(t, home, p, env, "hook-handler"); code != 0 {
			t.Fatalf("hook-handler exit %d: %s", code, stderr)
		}
	}
	seedCodexNotifyRollout(t, home, "019a0000-0000-7000-8000-0000000000ff")
	if _, _, code := runAgentDeckEnv(t, home, `{"type":"agent-turn-complete","thread-id":"019a0000-0000-7000-8000-0000000000ff","turn-id":"turn-1"}`, env, "codex-notify"); code != 0 {
		t.Fatalf("codex-notify exit %d", code)
	}
	if _, _, code := runAgentDeckEnv(t, home, traceE2EPayloads[2], append(env, "AGENTDECK_TRACE=off"), "trace", "hook"); code != 0 {
		t.Fatalf("trace hook exit %d", code)
	}
	if files := traceFiles(t, home); len(files) != 0 {
		t.Fatalf("level off wrote trace files: %v", files)
	}
}

// Codex: codex-notify's turn edge is traced when the level is on.
func TestTraceCodexNotifySpools(t *testing.T) {
	home := t.TempDir()
	env := []string{"AGENTDECK_TRACE=metadata", "AGENTDECK_INSTANCE_ID=inst-trace-codex", "AGENTDECK_TOOL=codex"}
	seedCodexNotifyRollout(t, home, "019a0000-0000-7000-8000-000000000077")
	if _, stderr, code := runAgentDeckEnv(t, home, `{"type":"agent-turn-complete","thread-id":"019a0000-0000-7000-8000-000000000077","turn-id":"turn-1"}`, env, "codex-notify"); code != 0 {
		t.Fatalf("codex-notify exit %d: %s", code, stderr)
	}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "trace", "--json")
	if code != 0 || !strings.Contains(stdout, `"TraceID":"codex:019a0000-0000-7000-8000-000000000077"`) {
		t.Fatalf("trace list exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

// The hook handler's full tracing call (level resolution, profile dir,
// normalize, append) — in-process, no binary start-up — stays far below
// 1ms median, both when on and when off (config read, nothing written).
func TestTraceSpoolFromHookMedianUnderOneMillisecond(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTDECK_PROFILE", "default")
	payload := []byte(traceE2EPayloads[2])
	for _, level := range []string{"summaries", ""} {
		t.Setenv("AGENTDECK_TRACE", level)
		const n = 200
		d := make([]time.Duration, n)
		for i := 0; i < n; i++ {
			start := time.Now()
			spoolTraceFromHook("inst-bench", payload, start)
			d[i] = time.Since(start)
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t.Logf("spoolTraceFromHook level=%q: median %v p95 %v", level, d[n/2], d[n*95/100])
		if d[n/2] >= time.Millisecond {
			t.Fatalf("level=%q median %v >= 1ms", level, d[n/2])
		}
	}
	if files := traceFiles(t, home); len(files) != 1 {
		t.Fatalf("expected one spool file, got %v", files)
	}
}

func BenchmarkTraceSpoolFromHook(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	b.Setenv("AGENTDECK_PROFILE", "default")
	b.Setenv("AGENTDECK_TRACE", "summaries")
	payload := []byte(traceE2EPayloads[2])
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		spoolTraceFromHook("inst-bench", payload, time.Now())
	}
}
